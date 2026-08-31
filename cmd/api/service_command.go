package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"IGoNotes/internal/service"
)

type userServiceManager interface {
	Install(context.Context, service.SystemdInstallOptions) (service.SystemdInstallResult, error)
	Uninstall(context.Context) error
}

type serverRunner func(context.Context, []string) error

func dispatchCommand(ctx context.Context, args []string, output io.Writer, manager userServiceManager, runServer serverRunner) error {
	if len(args) == 0 || args[0] != "service" {
		return runServer(ctx, args)
	}

	return runServiceCommand(ctx, args[1:], output, manager, filepath.Abs)
}

func runServiceCommand(
	ctx context.Context,
	args []string,
	output io.Writer,
	manager userServiceManager,
	absolutePath func(string) (string, error),
) error {
	if len(args) == 0 {
		return &commandLineError{err: fmt.Errorf("service command requires a subcommand: expected install or uninstall")}
	}

	switch args[0] {
	case "install":
		flags := flag.NewFlagSet("service install", flag.ContinueOnError)
		flags.SetOutput(output)
		port := flags.String("port", "8080", "port for the local server")
		configDir := flags.String("config", "", "configuration directory")
		base := flags.String("base", "", "base to open")
		if err := flags.Parse(args[1:]); err != nil {
			err = fmt.Errorf("parse service install options: %w", err)
			if errors.Is(err, flag.ErrHelp) {
				return err
			}
			return &commandLineError{err: err, reported: true}
		}
		if flags.NArg() != 0 {
			return &commandLineError{err: fmt.Errorf("service install does not accept operands: %q", flags.Args())}
		}

		if *configDir != "" {
			resolvedConfigDir, err := absolutePath(*configDir)
			if err != nil {
				return fmt.Errorf("make service config path absolute: %w", err)
			}
			*configDir = resolvedConfigDir
		}

		result, err := manager.Install(ctx, service.SystemdInstallOptions{
			Port:      *port,
			ConfigDir: *configDir,
			Base:      *base,
		})
		if err != nil {
			return fmt.Errorf("install service: %w", err)
		}

		if _, err := fmt.Fprintf(output, "IGoNotes service installed and started.\nURL: %s\nUnit: %s\nStatus: systemctl --user status %s\nLogs: journalctl --user-unit %s\n", result.URL, result.UnitPath, service.SystemdUserUnitName, service.SystemdUserUnitName); err != nil {
			return fmt.Errorf("write service install result: %w", err)
		}
		return nil

	case "uninstall":
		if len(args) != 1 {
			return &commandLineError{err: fmt.Errorf("service uninstall does not accept options or operands")}
		}
		if err := manager.Uninstall(ctx); err != nil {
			return fmt.Errorf("uninstall service: %w", err)
		}
		if _, err := fmt.Fprintln(output, "IGoNotes service removed. Notes and config are untouched."); err != nil {
			return fmt.Errorf("write service uninstall result: %w", err)
		}
		return nil

	default:
		return &commandLineError{err: fmt.Errorf("unknown service subcommand %q: expected install or uninstall", args[0])}
	}
}
