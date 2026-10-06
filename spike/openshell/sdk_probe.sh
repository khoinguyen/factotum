#!/usr/bin/env bash
#
# OpenShell Go SDK probe (task t-k3duthkrxo, decision V7 CLI vs SDK).
#
# Answers two questions with evidence:
#   1. Can the SDK reach the local gateway? The gateway helper refuses mTLS, so
#      this wires the CLI's mTLS material into v1.NewClient manually and runs a
#      command. Result: Exec works over mTLS with manual TLS config.
#   2. Is the SDK's file transfer usable? v1.FileInterface returns
#      ErrTransportNotAvailable in the open-source module (the SSH transport is
#      a stub), so upload/download via the SDK do not work.
#
# It creates its own throwaway sandbox unless FT_OS_SPIKE_SANDBOX names a live
# one. Run: mise run spike-openshell-sdk

set -u

SANDBOX="${FT_OS_SPIKE_SANDBOX:-}"
IMAGE="${FT_OS_SPIKE_IMAGE:-ghcr.io/anomalyco/opencode:latest}"
GATEWAY_DIR="${FT_OS_GATEWAY_DIR:-$HOME/.config/openshell/gateways/openshell/mtls}"
GATEWAY_ADDR="${FT_OS_GATEWAY_ADDR:-localhost:17670}"
SDK_VERSION="${FT_OS_SDK_VERSION:-12cec59bf4c3}"

WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/ft-os-sdk.XXXXXX")"
CREATED=""

cleanup() {
  if [ -n "$CREATED" ]; then
    openshell sandbox delete "$CREATED" >/dev/null 2>&1 || true
  fi
  rm -rf "$WORKDIR"
}
trap cleanup EXIT INT TERM

command -v openshell >/dev/null || {
  echo "openshell not found on PATH" >&2
  exit 2
}
command -v go >/dev/null || {
  echo "go not found on PATH" >&2
  exit 2
}
[ -f "$GATEWAY_DIR/ca.crt" ] || {
  echo "no mTLS material at $GATEWAY_DIR (is the local gateway configured?)" >&2
  exit 2
}

if [ -z "$SANDBOX" ]; then
  SANDBOX="ftos-sdk-$$"
  echo "=== create throwaway sandbox $SANDBOX ==="
  openshell sandbox create --name "$SANDBOX" --from "$IMAGE" --detach -- sh -c 'sleep 3600' || exit 1
  CREATED="$SANDBOX"
  i=0
  while [ "$i" -lt 30 ]; do
    openshell sandbox exec -n "$SANDBOX" --no-login-shell -- true >/dev/null 2>&1 && break
    i=$((i + 1))
    sleep 2
  done
fi

mkdir -p "$WORKDIR"
cat >"$WORKDIR/go.mod" <<'EOF'
module openshellsdkprobe

go 1.26.4
EOF

cat >"$WORKDIR/main.go" <<'EOF'
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: probe <sandbox>")
		os.Exit(2)
	}
	sandbox := os.Args[1]
	certDir := os.Getenv("FT_OS_GATEWAY_DIR")
	addr := os.Getenv("FT_OS_GATEWAY_ADDR")

	client, err := v1.NewClient(v1.Config{
		Address: addr,
		TLS: &v1.TLSConfig{
			CertFile: filepath.Join(certDir, "tls.crt"),
			KeyFile:  filepath.Join(certDir, "tls.key"),
			CAFile:   filepath.Join(certDir, "ca.crt"),
		},
		Auth:    v1.NoAuth(),
		Timeout: 15 * time.Second,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "NewClient:", err)
		os.Exit(1)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := client.Exec().Run(ctx, "default", sandbox, []string{"id"}, v1.ExecOptions{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "Exec:", err)
		os.Exit(1)
	}
	fmt.Printf("SDK Exec over mTLS -> %s", string(res.Stdout))

	err = client.Files().Download(ctx, "default", sandbox, "/etc/hostname", filepath.Join(os.TempDir(), "sdk-probe-hostname"))
	if errors.Is(err, v1.ErrTransportNotAvailable) {
		fmt.Println("SDK Files.Download -> ErrTransportNotAvailable (no transport in this release)")
	} else if err != nil {
		fmt.Println("SDK Files.Download ->", err)
	} else {
		fmt.Println("SDK Files.Download -> ok")
	}
}
EOF

cd "$WORKDIR"
echo "=== pin SDK $SDK_VERSION and build ==="
GOFLAGS=-mod=mod go get "github.com/NVIDIA/OpenShell/sdk/go@${SDK_VERSION}" 2>&1 | tail -5
echo "=== run probe against $SANDBOX at $GATEWAY_ADDR ==="
FT_OS_GATEWAY_DIR="$GATEWAY_DIR" FT_OS_GATEWAY_ADDR="$GATEWAY_ADDR" GOFLAGS=-mod=mod go run . "$SANDBOX"
