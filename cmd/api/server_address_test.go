package main

import (
	"net"
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
