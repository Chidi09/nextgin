//go:build js && wasm

package nextgin

import "net/http"

// Frontend is a stub on js/wasm targets where Next.js runs externally.
type Frontend struct {
	http.Handler
	Manifest *Manifest
}

// Close is a no-op on js/wasm targets.
func (f *Frontend) Close() error { return nil }
