package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

type fakeHTTPServerLifecycle struct {
	serve    func(net.Listener) error
	shutdown func(context.Context) error
	close    func() error
}

func (f *fakeHTTPServerLifecycle) Serve(listener net.Listener) error {
	return f.serve(listener)
}

func (f *fakeHTTPServerLifecycle) Shutdown(ctx context.Context) error {
	return f.shutdown(ctx)
}

func (f *fakeHTTPServerLifecycle) Close() error {
	return f.close()
}

func TestLocalServerEndpointUsesLoopback(t *testing.T) {
	for _, port := range []string{"8080", "9123"} {
		t.Run(port, func(t *testing.T) {
			address, browserURL := localServerEndpoint(port)
			wantAddress := net.JoinHostPort("127.0.0.1", port)

			if address != wantAddress {
				t.Errorf("localServerEndpoint() address = %q, want %q", address, wantAddress)
			}
			if browserURL != "http://"+wantAddress {
				t.Errorf("localServerEndpoint() URL = %q, want %q", browserURL, "http://"+wantAddress)
			}

			host, gotPort, err := net.SplitHostPort(address)
			if err != nil {
				t.Fatalf("net.SplitHostPort(%q) error = %v", address, err)
			}
			ip := net.ParseIP(host)
			if ip == nil || !ip.IsLoopback() {
				t.Errorf("localServerEndpoint() host = %q, want loopback IP", host)
			}
			if gotPort != port {
				t.Errorf("localServerEndpoint() port = %q, want %q", gotPort, port)
			}
		})
	}
}

func TestNewHTTPServerUsesHandler(t *testing.T) {
	handler := http.NewServeMux()
	server, ok := newHTTPServer(handler).(*http.Server)
	if !ok {
		t.Fatalf("newHTTPServer() type = %T, want *http.Server", server)
	}
	if server.Handler != handler {
		t.Fatal("newHTTPServer() did not configure the supplied handler")
	}
}

func TestServeLocalDoesNotSignalReadyOrServeWhenAddressIsOccupied(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer occupied.Close()

	var readyCalls atomic.Int32
	var serveCalls atomic.Int32
	server := &fakeHTTPServerLifecycle{
		serve: func(net.Listener) error {
			serveCalls.Add(1)
			return nil
		},
		shutdown: func(context.Context) error { return nil },
		close:    func() error { return nil },
	}
	err = serveLocal(
		context.Background(),
		occupied.Addr().String(),
		server,
		func() { readyCalls.Add(1) },
		time.Second,
	)
	if err == nil {
		t.Fatal("serveLocal() error = nil, want bind error")
	}
	if got := readyCalls.Load(); got != 0 {
		t.Errorf("ready callback calls = %d, want 0", got)
	}
	if got := serveCalls.Load(); got != 0 {
		t.Errorf("Serve() calls = %d, want 0", got)
	}
}

func TestServeLocalSignalsReadyBeforeServeAndClosesListener(t *testing.T) {
	wantErr := errors.New("stop serving")
	readyCalled := false
	listenerReceived := make(chan net.Listener, 1)
	server := &fakeHTTPServerLifecycle{
		serve: func(listener net.Listener) error {
			if !readyCalled {
				t.Error("serveLocal() called Serve before ready callback")
			}
			listenerReceived <- listener
			return wantErr
		},
		shutdown: func(context.Context) error { return nil },
		close:    func() error { return nil },
	}

	err := serveLocal(
		context.Background(),
		"127.0.0.1:0",
		server,
		func() { readyCalled = true },
		time.Second,
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("serveLocal() error = %v, want %v", err, wantErr)
	}
	listener := <-listenerReceived
	if _, err := listener.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("listener.Accept() error = %v, want %v", err, net.ErrClosed)
	}
}

func TestServeLocalCancellationShutsDownWithFreshDeadlineContext(t *testing.T) {
	type contextKey struct{}
	type contextState struct {
		hasDeadline bool
		err         error
		value       any
	}
	lifecycleContext, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "lifecycle"))
	serveStarted := make(chan struct{})
	stopServe := make(chan struct{})
	shutdownContextState := make(chan contextState, 1)
	var closeCalls atomic.Int32
	server := &fakeHTTPServerLifecycle{
		serve: func(net.Listener) error {
			close(serveStarted)
			<-stopServe
			return http.ErrServerClosed
		},
		shutdown: func(ctx context.Context) error {
			_, hasDeadline := ctx.Deadline()
			shutdownContextState <- contextState{
				hasDeadline: hasDeadline,
				err:         ctx.Err(),
				value:       ctx.Value(contextKey{}),
			}
			close(stopServe)
			return nil
		},
		close: func() error {
			closeCalls.Add(1)
			return nil
		},
	}
	go func() {
		<-serveStarted
		cancel()
	}()

	err := serveLocal(lifecycleContext, "127.0.0.1:0", server, func() {}, time.Minute)
	if err != nil {
		t.Fatalf("serveLocal() error = %v, want nil", err)
	}
	state := <-shutdownContextState
	if !state.hasDeadline {
		t.Error("Shutdown context has no deadline")
	}
	if state.err != nil {
		t.Errorf("Shutdown context error during successful shutdown = %v, want nil", state.err)
	}
	if got := state.value; got != nil {
		t.Errorf("Shutdown context inherited lifecycle value %v, want nil", got)
	}
	if got := closeCalls.Load(); got != 0 {
		t.Errorf("Close() calls = %d, want 0", got)
	}
}

func TestServeLocalShutdownErrorForcesCloseAndJoinsAllErrors(t *testing.T) {
	shutdownErr := errors.New("shutdown timeout")
	closeErr := errors.New("forced close failed")
	serveErr := errors.New("serve failed while stopping")
	lifecycleContext, cancel := context.WithCancel(context.Background())
	serveStarted := make(chan struct{})
	forceClosed := make(chan struct{})
	var closeCalls atomic.Int32
	server := &fakeHTTPServerLifecycle{
		serve: func(net.Listener) error {
			close(serveStarted)
			<-forceClosed
			return serveErr
		},
		shutdown: func(context.Context) error { return shutdownErr },
		close: func() error {
			closeCalls.Add(1)
			close(forceClosed)
			return closeErr
		},
	}
	go func() {
		<-serveStarted
		cancel()
	}()

	err := serveLocal(lifecycleContext, "127.0.0.1:0", server, func() {}, time.Second)
	for _, wantErr := range []error{shutdownErr, closeErr, serveErr} {
		if !errors.Is(err, wantErr) {
			t.Errorf("serveLocal() error = %v, want it to wrap %v", err, wantErr)
		}
	}
	if got := closeCalls.Load(); got != 1 {
		t.Errorf("Close() calls = %d, want 1", got)
	}
}

func TestServeLocalPropagatesUnexpectedServeError(t *testing.T) {
	wantErr := errors.New("unexpected serve failure")
	server := &fakeHTTPServerLifecycle{
		serve:    func(net.Listener) error { return wantErr },
		shutdown: func(context.Context) error { return nil },
		close:    func() error { return nil },
	}

	err := serveLocal(context.Background(), "127.0.0.1:0", server, func() {}, time.Second)
	if !errors.Is(err, wantErr) {
		t.Fatalf("serveLocal() error = %v, want %v", err, wantErr)
	}
}

func TestServeLocalNormalizesServerClosed(t *testing.T) {
	for _, serveErr := range []error{nil, http.ErrServerClosed} {
		server := &fakeHTTPServerLifecycle{
			serve:    func(net.Listener) error { return serveErr },
			shutdown: func(context.Context) error { return nil },
			close:    func() error { return nil },
		}

		if err := serveLocal(context.Background(), "127.0.0.1:0", server, func() {}, time.Second); err != nil {
			t.Errorf("serveLocal() error = %v for Serve error %v, want nil", err, serveErr)
		}
	}
}
