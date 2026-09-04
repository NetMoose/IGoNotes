package git

import (
	"bytes"
	"context"
	"reflect"
	"testing"
)

type recordingRunner struct {
	commands []Command
}

func TestServiceRunForwardsStdin(t *testing.T) {
	runner := &recordingRunner{}
	service := NewService(runner, unusedPorcelain{})
	input := bytes.NewBufferString("conflict-path\x00")

	if _, err := service.runLocalInput(context.Background(), "/canonical/notes", false, input, "add", "--stdin"); err != nil {
		t.Fatalf("runLocalInput() error = %v", err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.commands))
	}
	if runner.commands[0].Stdin != input {
		t.Fatal("Service did not forward the original stdin reader")
	}
}

func (r *recordingRunner) Run(_ context.Context, command Command) (Result, error) {
	r.commands = append(r.commands, command)
	return Result{Stdout: "ok"}, nil
}

type unusedPorcelain struct{}

func (unusedPorcelain) Version(context.Context, string) (Version, error) {
	return Version{}, nil
}

func (unusedPorcelain) ValidateBranch(context.Context, string, string) error { return nil }

func (unusedPorcelain) InspectLocal(context.Context, string) (LocalInspection, error) {
	return LocalInspection{}, nil
}

func (unusedPorcelain) InspectRemote(context.Context, string, string) (RemoteInspection, error) {
	return RemoteInspection{}, nil
}

func (unusedPorcelain) HistoryRelation(context.Context, string, string) (string, error) {
	return "", nil
}

func TestServiceRunForwardsLandedCommand(t *testing.T) {
	runner := &recordingRunner{}
	service := NewService(runner, unusedPorcelain{})
	args := []string{"push", "origin", "abc:refs/heads/main"}
	remoteURL := "https://user:token@example.com/notes.git"

	if _, err := service.run(context.Background(), "/canonical/notes", NetworkOperation, false, remoteURL, nil, args...); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	args[0] = "mutated"
	if len(runner.commands) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.commands))
	}
	want := Command{
		Dir:      "/canonical/notes",
		Args:     []string{"push", "origin", "abc:refs/heads/main"},
		Scope:    NetworkOperation,
		ReadOnly: false,
		Secrets:  []string{remoteURL},
	}
	if !reflect.DeepEqual(runner.commands[0], want) {
		t.Fatalf("forwarded command = %#v, want %#v", runner.commands[0], want)
	}

	runner.commands[0].Secrets[0] = "mutated secret"
	if remoteURL != "https://user:token@example.com/notes.git" {
		t.Fatal("runner command aliases caller remote URL")
	}

	localArgs := []string{"status", "--short"}
	if _, err := service.runLocal(context.Background(), "/canonical/notes", true, localArgs...); err != nil {
		t.Fatalf("runLocal() error = %v", err)
	}
	localArgs[0] = "mutated"
	wantLocal := Command{
		Dir:      "/canonical/notes",
		Args:     []string{"status", "--short"},
		Scope:    LocalOperation,
		ReadOnly: true,
		Secrets:  nil,
	}
	if !reflect.DeepEqual(runner.commands[1], wantLocal) {
		t.Fatalf("local command = %#v, want %#v", runner.commands[1], wantLocal)
	}

	networkArgs := []string{"fetch", "origin"}
	if _, err := service.runNetwork(context.Background(), "/canonical/notes", remoteURL, true, networkArgs...); err != nil {
		t.Fatalf("runNetwork() error = %v", err)
	}
	networkArgs[0] = "mutated"
	wantNetwork := Command{
		Dir:      "/canonical/notes",
		Args:     []string{"fetch", "origin"},
		Scope:    NetworkOperation,
		ReadOnly: true,
		Secrets:  []string{remoteURL},
	}
	if !reflect.DeepEqual(runner.commands[2], wantNetwork) {
		t.Fatalf("network command = %#v, want %#v", runner.commands[2], wantNetwork)
	}
}
