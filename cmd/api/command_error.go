package main

import (
	"errors"
	"flag"
)

type commandLineError struct {
	err      error
	reported bool
}

func (e *commandLineError) Error() string {
	return e.err.Error()
}

func (e *commandLineError) Unwrap() error {
	return e.err
}

func commandExitCode(err error) int {
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return 0
	}
	var usageErr *commandLineError
	if errors.As(err, &usageErr) {
		return 2
	}
	return 1
}

func shouldLogCommandError(err error) bool {
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return false
	}
	var usageErr *commandLineError
	return !errors.As(err, &usageErr) || !usageErr.reported
}
