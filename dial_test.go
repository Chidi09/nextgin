//go:build !js

package nextgin

import (
	"context"
	"io"
	"net"
	"slices"
	"testing"
)

func TestCheckTCPNetworkAndDialRejections(t *testing.T) {
	tests := []struct {
		network string
		wantErr string
	}{
		{network: "tcp"},
		{network: "tcp4"},
		{network: "tcp6"},
		{network: "udp", wantErr: `nextgin: DialContext supports tcp only, got "udp"`},
		{network: "unix", wantErr: `nextgin: DialContext supports tcp only, got "unix"`},
	}

	for _, tc := range tests {
		t.Run(tc.network, func(t *testing.T) {
			err := checkTCPNetwork(tc.network)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("checkTCPNetwork(%q) unexpected error: %v", tc.network, err)
				}
				return
			}
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("checkTCPNetwork(%q) = %v, want %q", tc.network, err, tc.wantErr)
			}

			conn, dialErr := DialContext(context.Background(), tc.network, "127.0.0.1:0")
			if conn != nil {
				_ = conn.Close()
			}
			if dialErr == nil || dialErr.Error() != tc.wantErr {
				t.Fatalf("DialContext(%q) = %v, want %q", tc.network, dialErr, tc.wantErr)
			}
		})
	}
}

func TestDialContextEchoAndPostDialCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	srvErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			srvErr <- err
			return
		}
		defer conn.Close()
		_, err = io.Copy(conn, conn)
		srvErr <- err
	}()

	dialCtx, cancel := context.WithCancel(context.Background())
	conn, err := DialContext(dialCtx, "tcp", ln.Addr().String())
	if err != nil {
		cancel()
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	assertEcho := func(msg string) {
		t.Helper()
		if _, err := io.WriteString(conn, msg); err != nil {
			t.Fatalf("write %q: %v", msg, err)
		}
		buf := make([]byte, len(msg))
		if _, err := io.ReadFull(conn, buf); err != nil {
			t.Fatalf("read %q: %v", msg, err)
		}
		if string(buf) != msg {
			t.Fatalf("echo = %q, want %q", string(buf), msg)
		}
	}

	assertEcho("hello\n")

	// Mirror go-redis: cancel the dial context right after dial returns.
	cancel()
	assertEcho("after-cancel\n")

	_ = conn.Close()
	if err := <-srvErr; err != nil {
		t.Fatalf("server copy: %v", err)
	}
}

func TestDialContextCancelled(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	conn, err := DialContext(ctx, "tcp", ln.Addr().String())
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("expected error for already-cancelled context, got nil")
	}
}

func TestLookupHost(t *testing.T) {
	addrs, err := LookupHost(context.Background(), "127.0.0.1")
	if err != nil {
		t.Fatalf("LookupHost: %v", err)
	}
	if !slices.Contains(addrs, "127.0.0.1") {
		t.Fatalf("LookupHost(%q) = %v, want containing %q", "127.0.0.1", addrs, "127.0.0.1")
	}
}
