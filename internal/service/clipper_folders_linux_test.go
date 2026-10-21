package service

import (
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"IGoNotes/internal/model"
)

func TestClipperDestinationFoldersNeverOpenMarkdownContent(t *testing.T) {
	f := newClipperDestinationFixture(t)
	// Opening this .md for reading would block forever: no writer exists. Unlike
	// chmod(000), the assertion also works for privileged test processes.
	if err := syscall.Mkfifo(filepath.Join(f.target, "do-not-read.md"), 0600); err != nil {
		t.Fatal(err)
	}
	type result struct {
		response model.ClipperFoldersResponse
		err      error
	}
	done := make(chan result, 1)
	go func() { response, err := f.service.Folders("target"); done <- result{response, err} }()
	select {
	case got := <-done:
		if got.err != nil || !reflect.DeepEqual(got.response.Folders, []string{""}) {
			t.Fatalf("folders with Markdown FIFO = %+v, %v", got.response, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("folder discovery attempted to open Markdown content")
	}
}
