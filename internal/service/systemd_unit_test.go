package service

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderSystemdUserUnit(t *testing.T) {
	t.Run("renders all arguments", func(t *testing.T) {
		got, err := renderSystemdUserUnit("/opt/IGo Notes/igo%notes", SystemdInstallOptions{
			Port:      "8080",
			ConfigDir: "/home/user/My Config/$current",
			Base:      `Base "quoted"\path`,
		})
		if err != nil {
			t.Fatalf("renderSystemdUserUnit() error = %v, want nil", err)
		}

		want := []byte(`# Managed by IGoNotes
[Unit]
Description=IGoNotes local note server

[Service]
Type=simple
ExecStart=:"/opt/IGo Notes/igo%%notes" "--port" "8080" "--config" "/home/user/My Config/$current" "--base" "Base \"quoted\"\\path" "--no-browser"
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
`)
		if !bytes.Equal(got, want) {
			t.Errorf("renderSystemdUserUnit() =\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("omits empty optional arguments", func(t *testing.T) {
		got, err := renderSystemdUserUnit("/usr/bin/igonotes", SystemdInstallOptions{Port: "9000"})
		if err != nil {
			t.Fatalf("renderSystemdUserUnit() error = %v, want nil", err)
		}

		want := []byte(`# Managed by IGoNotes
[Unit]
Description=IGoNotes local note server

[Service]
Type=simple
ExecStart=:"/usr/bin/igonotes" "--port" "9000" "--no-browser"
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
`)
		if !bytes.Equal(got, want) {
			t.Errorf("renderSystemdUserUnit() =\n%s\nwant:\n%s", got, want)
		}
	})
}

func TestQuoteSystemdExecArg(t *testing.T) {
	t.Run("escapes systemd syntax and ASCII controls", func(t *testing.T) {
		value := "back\\slash\" percent% dollar$ line\nreturn\rtab\tcontrols\x01\x1f\x7f unicode Привет"
		got, err := quoteSystemdExecArg(value)
		if err != nil {
			t.Fatalf("quoteSystemdExecArg() error = %v, want nil", err)
		}

		want := `"back\\slash\" percent%% dollar$ line\nreturn\rtab\tcontrols\x01\x1F\x7F unicode Привет"`
		if got != want {
			t.Errorf("quoteSystemdExecArg() = %q, want %q", got, want)
		}
	})

	t.Run("rejects NUL", func(t *testing.T) {
		_, err := quoteSystemdExecArg("before\x00after")
		if err == nil {
			t.Fatal("quoteSystemdExecArg() error = nil, want non-nil")
		}
		if !strings.Contains(err.Error(), "NUL") {
			t.Errorf("quoteSystemdExecArg() error = %q, want mention of NUL", err)
		}
	})
}
