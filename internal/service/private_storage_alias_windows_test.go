//go:build windows

package service

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"IGoNotes/internal/model"
)

func TestPrivateStorageSubstMissingStagingAddBeforeMkdir(t *testing.T) {
	f := newPrivateSettingsFixture(t, true)
	var drive string
	for letter := 'Z'; letter >= 'P'; letter-- {
		candidate := string(letter) + ":"
		if _, err := os.Stat(candidate + `\`); !os.IsNotExist(err) {
			continue
		}
		if err := exec.Command("subst", candidate, f.data).Run(); err != nil {
			continue
		}
		drive = candidate
		break
	}
	if drive == "" {
		t.Skip("no available drive or normal-user subst unavailable")
	}
	t.Cleanup(func() {
		if out, err := exec.Command("subst", drive, "/D").CombinedOutput(); err != nil {
			t.Errorf("remove subst: %v %s", err, out)
		}
	})
	alias := drive + `\`
	if !privatePathContains(f.staging, filepath.Join(alias, "clipper-staging", "upload")) {
		t.Fatal("missing native-alias descendant not detected")
	}
	if privatePathContains(filepath.Join(f.staging, "upload"), filepath.Join(alias, "clipper-staging")) {
		t.Fatal("reversed containment")
	}
	before, err := os.ReadFile(f.store.configPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeConfig := f.settings.GetConfig()
	_, err = f.settings.AddBase(model.BaseMutationRequest{Mode: "create", Name: "clipper-staging", Path: alias})
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("unsafe Add = %v", err)
	}
	for _, path := range []string{f.staging, filepath.Join(alias, "clipper-staging")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("created staging before rejection: %s: %v", path, err)
		}
	}
	after, err := os.ReadFile(f.store.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || !reflect.DeepEqual(beforeConfig, f.settings.GetConfig()) {
		t.Fatal("configuration changed")
	}
	if f.runtime.persistCalls != 0 || len(f.runtime.transactionCalls) != 0 {
		t.Fatal("runtime changed")
	}
}
