package fake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/khoinguyen/factotum/pkg/isolation"
	"github.com/khoinguyen/factotum/pkg/isolation/fake"
)

func TestName(t *testing.T) {
	if got := fake.New("mem").Name(); got != "mem" {
		t.Fatalf("Name() = %q, want mem", got)
	}
}

func TestPrepareStagesFilesAndReturnsHandle(t *testing.T) {
	ctx := context.Background()
	b := fake.New("mem")
	handle, err := b.Prepare(ctx, isolation.Spec{
		Image:   isolation.Image{Ref: "img:1", User: "1000"},
		Workdir: "/work",
		Files:   []isolation.File{{Path: "/work/in.txt", Content: []byte("hi"), Mode: 0o644}},
		Labels:  map[string]string{"task": "t-1"},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if handle.ID() == "" {
		t.Fatal("Prepare() handle has empty id")
	}
	prepared := b.Prepared()
	if len(prepared) != 1 {
		t.Fatalf("Prepared() = %d specs, want 1", len(prepared))
	}
	if got := prepared[0].Image.Ref; got != "img:1" {
		t.Errorf("prepared image = %q, want img:1", got)
	}
	files, err := b.Download(ctx, handle, []string{"/work/in.txt"})
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if len(files) != 1 || string(files[0].Content) != "hi" {
		t.Fatalf("Download() = %#v, want staged file", files)
	}
}

func TestPrepareIDsAreDeterministicPerBackend(t *testing.T) {
	ctx := context.Background()
	b := fake.New("mem")
	first, _ := b.Prepare(ctx, isolation.Spec{})
	second, _ := b.Prepare(ctx, isolation.Spec{})
	if first.ID() == second.ID() {
		t.Fatalf("handles share id %q, want distinct", first.ID())
	}
}

func TestExecStreamsEventsAndReturnsResult(t *testing.T) {
	ctx := context.Background()
	b := fake.New("mem")
	b.Program(isolation.ExecResult{ExitCode: 0, Stdout: []byte("hello\n")},
		isolation.Event{Kind: isolation.EventOutput, Stream: isolation.StreamStdout, Message: "hello\n"},
		isolation.Event{Kind: isolation.EventStatus, Message: "exited"},
	)
	handle, _ := b.Prepare(ctx, isolation.Spec{})
	exec, err := b.Exec(ctx, handle, isolation.Command{Argv: []string{"echo", "hello"}})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}

	var kinds []isolation.EventKind
	for ev := range exec.Events() {
		kinds = append(kinds, ev.Kind)
	}
	if len(kinds) != 2 {
		t.Fatalf("streamed %d events, want 2", len(kinds))
	}
	res, err := exec.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if string(res.Stdout) != "hello\n" {
		t.Errorf("Wait() stdout = %q, want hello", res.Stdout)
	}
	cmds := b.Commands()
	if len(cmds) != 1 || cmds[0].Argv[0] != "echo" {
		t.Fatalf("Commands() = %#v, want recorded echo", cmds)
	}
}

func TestUploadAddsFiles(t *testing.T) {
	ctx := context.Background()
	b := fake.New("mem")
	handle, _ := b.Prepare(ctx, isolation.Spec{})
	if err := b.Upload(ctx, handle, []isolation.File{{Path: "/work/up.txt", Content: []byte("body")}}); err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	files, err := b.Download(ctx, handle, []string{"/work/up.txt", "/work/missing"})
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if len(files) != 1 || string(files[0].Content) != "body" {
		t.Fatalf("Download() = %#v, want only the uploaded file", files)
	}
}

func TestLogsReturnsEvents(t *testing.T) {
	ctx := context.Background()
	b := fake.New("mem")
	b.Program(isolation.ExecResult{}, isolation.Event{Kind: isolation.EventPolicy, Message: "denied example.com"})
	handle, _ := b.Prepare(ctx, isolation.Spec{})
	ch, err := b.Logs(ctx, handle, isolation.LogOptions{})
	if err != nil {
		t.Fatalf("Logs() error = %v", err)
	}
	var got int
	for range ch {
		got++
	}
	if got != 1 {
		t.Fatalf("Logs() streamed %d events, want 1", got)
	}
}

func TestPolicyCredentialsStopDeleteRecorded(t *testing.T) {
	ctx := context.Background()
	b := fake.New("mem")
	handle, _ := b.Prepare(ctx, isolation.Spec{})
	if err := b.ApplyPolicy(ctx, handle, isolation.Policy{DefaultDeny: true, AllowHosts: []string{"git.example"}}); err != nil {
		t.Fatalf("ApplyPolicy() error = %v", err)
	}
	if err := b.AttachCredential(ctx, handle, isolation.Credential{Provider: "openrouter", EnvVar: "OPENROUTER_API_KEY"}); err != nil {
		t.Fatalf("AttachCredential() error = %v", err)
	}
	if err := b.Stop(ctx, handle); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := b.Delete(ctx, handle); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if got := b.Policies(); len(got) != 1 || !got[0].DefaultDeny {
		t.Fatalf("Policies() = %#v, want one deny-by-default", got)
	}
	if got := b.Credentials(); len(got) != 1 || got[0].Provider != "openrouter" {
		t.Fatalf("Credentials() = %#v, want openrouter", got)
	}
	if len(b.Stopped()) != 1 || len(b.Deleted()) != 1 {
		t.Fatalf("Stopped()=%v Deleted()=%v, want one each", b.Stopped(), b.Deleted())
	}
}

func TestFailWithPropagates(t *testing.T) {
	ctx := context.Background()
	want := errors.New("boom")
	b := fake.New("mem")
	b.FailWith(want)
	if _, err := b.Prepare(ctx, isolation.Spec{}); !errors.Is(err, want) {
		t.Fatalf("Prepare() error = %v, want %v", err, want)
	}
}
