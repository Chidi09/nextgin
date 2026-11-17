//go:build !js

package nextgin

import (
	"context"
	"net"
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
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

// LookupHost resolves host addresses for drivers such as pgx (LookupFunc).
// On Cloudflare, Go wasm has no DNS resolver and Workers connect() resolves
// names itself, so LookupHost returns host unchanged.
func LookupHost(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}
