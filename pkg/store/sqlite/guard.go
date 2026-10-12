package sqlite

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"

	"github.com/khoinguyen/factotum/pkg/core"
)

// WAL sidecar geometry. SQLite's unix shared-memory region is 32 KiB
// (unixShmRegionPerMap's SHMSZ), and the -shm file is grown in whole regions.
// A -shm of any other size was truncated while a connection held it
// memory-mapped; the next WAL header read then touches pages past EOF and the
// process dies with SIGBUS instead of returning an error.
const (
	shmRegionSize = 32 * 1024
	walHeaderSize = 32
)

// staleWAL reports an error when path's WAL sidecars are malformed: a -shm that
// is not a whole number of regions, or a non-empty -wal shorter than its header.
// Handing such a sidecar to SQLite would memory-map the stale wal-index and
// crash the process, so the backend detects it first and returns an error the
// caller renders as its 503 contract.
func staleWAL(path string) error {
	if path == "" || path == ":memory:" {
		return nil
	}
	if err := malformedSidecar(path+"-shm", func(size int64) bool {
		return size < shmRegionSize || size%shmRegionSize != 0
	}, "wal-index"); err != nil {
		return err
	}
	return malformedSidecar(path+"-wal", func(size int64) bool {
		return size > 0 && size < walHeaderSize
	}, "write-ahead log")
}

func malformedSidecar(file string, malformed func(int64) bool, what string) error {
	fi, err := os.Stat(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("sqlite: inspect %s %s: %w", what, file, err)
	}
	if malformed(fi.Size()) {
		return fmt.Errorf("%w: sqlite %s %s is malformed (%d bytes); the database was truncated under a live connection", core.ErrInvalid, what, file, fi.Size())
	}
	return nil
}

// repairShm restores the -shm to a whole number of regions when it has been
// truncated. SQLite may already hold the file memory-mapped; growing the file
// back to at least one region makes every mapped page valid again, so the
// close-time checkpoint and any later access return cleanly instead of
// faulting on a page past EOF. The -shm is a regenerable cache, never
// authoritative, so extending it cannot lose committed data.
func repairShm(path string) {
	file := path + "-shm"
	fi, err := os.Stat(file)
	if err != nil || (fi.Size() >= shmRegionSize && fi.Size()%shmRegionSize == 0) {
		return
	}
	size := fi.Size()
	if size < shmRegionSize {
		size = shmRegionSize
	} else {
		size = (size/shmRegionSize + 1) * shmRegionSize
	}
	_ = os.Truncate(file, size)
}

// guardedConnector wraps a driver.Connector so every database operation first
// checks for a stale WAL. It is the single choke point all queries, execs, and
// transactions pass through, so an externally truncated sidecar yields an error
// rather than a SIGBUS.
type guardedConnector struct {
	base driver.Connector
	path string
}

func (c *guardedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &guardedConn{Conn: conn, path: c.path}, nil
}

func (c *guardedConnector) Driver() driver.Driver { return c.base.Driver() }

// guardedConn checks the WAL sidecars before each operation and otherwise
// delegates to the wrapped connection.
type guardedConn struct {
	driver.Conn
	path string
}

// guardWAL detects a stale WAL and, when found, restores the -shm length so any
// mapping SQLite already holds stays valid; it returns the detection error.
func guardWAL(path string) error {
	err := staleWAL(path)
	if err != nil {
		repairShm(path)
	}
	return err
}

func (c *guardedConn) check() error { return guardWAL(c.path) }

func (c *guardedConn) Close() error {
	_ = guardWAL(c.path)
	return c.Conn.Close()
}

// PrepareContext guards the context path database/sql uses. The base connection
// always implements it; the ErrSkip fallback (to the embedded, deprecated
// Prepare) exists only so the interface contract holds for other connectors.
func (c *guardedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	prep, ok := c.Conn.(driver.ConnPrepareContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	stmt, err := prep.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return &guardedStmt{Stmt: stmt, path: c.path}, nil
}

func (c *guardedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return q.QueryContext(ctx, query, args)
}

func (c *guardedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	e, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return e.ExecContext(ctx, query, args)
}

func (c *guardedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	b, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, driver.ErrSkip
	}
	return b.BeginTx(ctx, opts)
}

func (c *guardedConn) Ping(ctx context.Context) error {
	if err := c.check(); err != nil {
		return err
	}
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *guardedConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *guardedConn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

// guardedStmt re-checks the WAL before each execution, so a prepared statement
// run after the sidecars go stale still fails cleanly.
type guardedStmt struct {
	driver.Stmt
	path string
}

func (s *guardedStmt) check() error { return guardWAL(s.path) }

func (s *guardedStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	q, ok := s.Stmt.(driver.StmtQueryContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return q.QueryContext(ctx, args)
}

func (s *guardedStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	e, ok := s.Stmt.(driver.StmtExecContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return e.ExecContext(ctx, args)
}
