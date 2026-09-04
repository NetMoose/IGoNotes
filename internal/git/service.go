package git

import (
	"context"
	"io"
	"time"
)

type Service struct {
	runner    Runner
	porcelain Porcelain
	now       func() time.Time
}

func NewService(runner Runner, porcelain Porcelain) *Service {
	if runner == nil {
		panic("git: nil Runner")
	}
	if porcelain == nil {
		panic("git: nil Porcelain")
	}
	return &Service{runner: runner, porcelain: porcelain, now: time.Now}
}

func (s *Service) run(
	ctx context.Context,
	dir string,
	scope OperationScope,
	readOnly bool,
	remoteURL string,
	stdin io.Reader,
	args ...string,
) (Result, error) {
	secrets := make([]string, 0, 1)
	if remoteURL != "" {
		secrets = append(secrets, remoteURL)
	}
	return s.runner.Run(ctx, Command{
		Dir:      dir,
		Args:     append([]string(nil), args...),
		Scope:    scope,
		ReadOnly: readOnly,
		Secrets:  append([]string(nil), secrets...),
		Stdin:    stdin,
	})
}

func (s *Service) runLocal(ctx context.Context, dir string, readOnly bool, args ...string) (Result, error) {
	return s.run(ctx, dir, LocalOperation, readOnly, "", nil, args...)
}

func (s *Service) runNetwork(ctx context.Context, dir, remoteURL string, readOnly bool, args ...string) (Result, error) {
	return s.run(ctx, dir, NetworkOperation, readOnly, remoteURL, nil, args...)
}

func (s *Service) runLocalInput(ctx context.Context, dir string, readOnly bool, stdin io.Reader, args ...string) (Result, error) {
	return s.run(ctx, dir, LocalOperation, readOnly, "", stdin, args...)
}
