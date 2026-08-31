package service

import (
	"fmt"
	"strings"
)

const (
	SystemdUserUnitName = "igonotes.service"
	systemdUnitMarker   = "# Managed by IGoNotes"
)

type SystemdInstallOptions struct {
	Port      string
	ConfigDir string
	Base      string
}

func renderSystemdUserUnit(executable string, options SystemdInstallOptions) ([]byte, error) {
	argv := []string{executable, "--port", options.Port}
	if options.ConfigDir != "" {
		argv = append(argv, "--config", options.ConfigDir)
	}
	if options.Base != "" {
		argv = append(argv, "--base", options.Base)
	}
	argv = append(argv, "--no-browser")

	quoted := make([]string, len(argv))
	for i, arg := range argv {
		var err error
		quoted[i], err = quoteSystemdExecArg(arg)
		if err != nil {
			return nil, err
		}
	}

	unit := systemdUnitMarker + `
[Unit]
Description=IGoNotes local note server

[Service]
Type=simple
ExecStart=:` + strings.Join(quoted, " ") + `
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
`
	return []byte(unit), nil
}

func quoteSystemdExecArg(value string) (string, error) {
	const hex = "0123456789ABCDEF"

	var quoted strings.Builder
	quoted.Grow(len(value) + 2)
	quoted.WriteByte('"')
	for i := 0; i < len(value); i++ {
		char := value[i]
		switch char {
		case 0:
			return "", fmt.Errorf("systemd ExecStart argument contains NUL")
		case '\\':
			quoted.WriteString(`\\`)
		case '"':
			quoted.WriteString(`\"`)
		case '%':
			quoted.WriteString("%%")
		case '\n':
			quoted.WriteString(`\n`)
		case '\r':
			quoted.WriteString(`\r`)
		case '\t':
			quoted.WriteString(`\t`)
		default:
			if char < 0x20 || char == 0x7f {
				quoted.WriteString(`\x`)
				quoted.WriteByte(hex[char>>4])
				quoted.WriteByte(hex[char&0x0f])
			} else {
				quoted.WriteByte(char)
			}
		}
	}
	quoted.WriteByte('"')
	return quoted.String(), nil
}
