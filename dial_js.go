//go:build js && wasm

package nextgin

import (
	"context"
	"net"

	"github.com/syumai/workers-go/cloudflare/sockets"
)

// DialContext opens a plain TCP connection ("tcp", "tcp4", or "tcp6") for
// database and cache drivers such as pgx (DialFunc) and go-redis (Dialer).
//
// It does not add TLS: let the driver handle TLS itself (for example, pgx
// sslmode=require or go-redis TLSConfig).
//
// Cloudflare caveat: on the Cloudflare target, a connection cannot be reused
// across requests; open one per request and close it, and put a connection
// pooler such as PgBouncer or a Neon pooler in front.
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if err := checkTCPNetwork(network); err != nil {
		return nil, err
	}
	// workers-go ties the socket's lifetime to the context passed to Connect,
	// and go-redis cancels its dial context right after the dial returns, which
	// otherwise makes every read fail with "i/o timeout".
	ctx = context.WithoutCancel(ctx)
	return sockets.Connect(ctx, addr, &sockets.SocketOptions{SecureTransport: sockets.SecureTransportOff})
}

// LookupHost resolves host addresses for drivers such as pgx (LookupFunc).
// On Cloudflare, Go wasm has no DNS resolver and Workers connect() resolves
// names itself, so LookupHost returns host unchanged.
func LookupHost(_ context.Context, host string) ([]string, error) {
	return []string{host}, nil
}
