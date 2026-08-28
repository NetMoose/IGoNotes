package main

import (
	"errors"
	"net"
	"net/http"
	"testing"
)

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

func TestServeLocalDoesNotSignalReadyWhenAddressIsOccupied(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer occupied.Close()

	readyCalled := false
	serveCalled := false
	err = serveLocal(
		occupied.Addr().String(),
		http.NotFoundHandler(),
		func() { readyCalled = true },
		func(net.Listener, http.Handler) error {
			serveCalled = true
			return nil
		},
	)
	if err == nil {
		t.Fatal("serveLocal() error = nil, want bind error")
	}
	if readyCalled {
		t.Error("serveLocal() called ready callback after bind failure")
	}
	if serveCalled {
		t.Error("serveLocal() called serve after bind failure")
	}
}

func TestServeLocalSignalsReadyBeforeServeAndClosesListener(t *testing.T) {
	wantErr := errors.New("stop serving")
	readyCalled := false
	var listener net.Listener

	err := serveLocal(
		"127.0.0.1:0",
		http.NotFoundHandler(),
		func() { readyCalled = true },
		func(gotListener net.Listener, _ http.Handler) error {
			listener = gotListener
			if !readyCalled {
				t.Error("serveLocal() called serve before ready callback")
			}
			return wantErr
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("serveLocal() error = %v, want %v", err, wantErr)
	}
	if listener == nil {
		t.Fatal("serveLocal() did not pass listener to serve")
	}
	if _, err := listener.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("listener.Accept() error = %v, want %v", err, net.ErrClosed)
	}
}
