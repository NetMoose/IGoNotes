//go:build !windows

package main

import (
	"os"
	"syscall"
	"testing"
)

func TestShutdownSignalsIncludeInterruptAndTerminate(t *testing.T) {
	got := shutdownSignals()
	want := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if len(got) != len(want) {
		t.Fatalf("shutdownSignals() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("shutdownSignals()[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}
