package conformance

import (
	"context"
	"reflect"
	"testing"

	"github.com/khoinguyen/factotum/pkg/isolation"
)

// TestUnsupportedHonorsDeclaredCapabilities proves the capability options change
// the contract: a backend that declares Logs and Policy must implement them, and
// the suite accepts that instead of demanding ErrUnsupported.
func TestUnsupportedHonorsDeclaredCapabilities(t *testing.T) {
	be := &capBackend{logs: true, policy: true}
	testUnsupported(t, be, config{logs: true, policy: true})
}

// TestUnsupportedRequiresErrUnsupported proves an undeclared optional operation
// must return ErrUnsupported, not another error and not a silent success.
func TestUnsupportedRequiresErrUnsupported(t *testing.T) {
	be := &capBackend{}
	testUnsupported(t, be, config{})
}

// capBackend is the minimal IsolationBackend the capability tests drive. It
// implements Logs and Policy only when configured to, and ErrUnsupported
// otherwise, so both branches of testUnsupported are exercised.
type capBackend struct {
	logs   bool
	policy bool
}

func (b *capBackend) Name() string { return "cap" }

func (b *capBackend) Prepare(_ context.Context, spec isolation.Spec) (isolation.Handle, error) {
	if !b.policy && !reflect.DeepEqual(spec.Policy, isolation.Policy{}) {
		return nil, isolation.ErrUnsupported
	}
	return capHandle{}, nil
}

func (b *capBackend) Exec(_ context.Context, _ isolation.Handle, cmd isolation.Command) (isolation.Execution, error) {
	if cmd.TTY {
		return nil, isolation.ErrUnsupported
	}
	ch := make(chan isolation.Event)
	close(ch)
	return &capExecution{events: ch}, nil
}

func (b *capBackend) Upload(context.Context, isolation.Handle, []isolation.File) error { return nil }

func (b *capBackend) Download(context.Context, isolation.Handle, []string) ([]isolation.File, error) {
	return nil, nil
}

func (b *capBackend) Logs(context.Context, isolation.Handle, isolation.LogOptions) (<-chan isolation.Event, error) {
	if !b.logs {
		return nil, isolation.ErrUnsupported
	}
	ch := make(chan isolation.Event)
	close(ch)
	return ch, nil
}

func (b *capBackend) Stop(context.Context, isolation.Handle) error   { return nil }
func (b *capBackend) Delete(context.Context, isolation.Handle) error { return nil }

func (b *capBackend) ApplyPolicy(_ context.Context, _ isolation.Handle, p isolation.Policy) error {
	if b.policy || reflect.DeepEqual(p, isolation.Policy{}) {
		return nil
	}
	return isolation.ErrUnsupported
}

func (b *capBackend) AttachCredential(context.Context, isolation.Handle, isolation.Credential) error {
	return isolation.ErrUnsupported
}

type capHandle struct{}

func (capHandle) ID() string { return "cap-1" }

type capExecution struct{ events <-chan isolation.Event }

func (e *capExecution) Events() <-chan isolation.Event { return e.events }

func (e *capExecution) Wait(context.Context) (isolation.ExecResult, error) {
	return isolation.ExecResult{}, nil
}

var _ isolation.IsolationBackend = (*capBackend)(nil)
