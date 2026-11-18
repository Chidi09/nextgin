package nextgin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// DefaultMaxBodyBytes is the default maximum request body size (1 MiB) enforced
// by ReadJSON and ReadRawBody.
const DefaultMaxBodyBytes int64 = 1 << 20

// ErrBodyTooLarge is returned (wrapped with *http.MaxBytesError) when a request
// body exceeds the configured byte limit.
var ErrBodyTooLarge = errors.New("nextgin: request body too large")

// ReadJSON decodes the JSON request body into dst using encoding/json, capped
// at DefaultMaxBodyBytes (1 MiB). Trailing data after the JSON value is rejected.
func ReadJSON(r *http.Request, dst any) error {
	return ReadJSONLimit(nil, r, dst, DefaultMaxBodyBytes)
}

// ReadJSONLimit decodes the JSON request body into dst, capping reads at
// maxBytes (defaults to DefaultMaxBodyBytes when <= 0). Passing w lets the
// server close oversized connections immediately via http.MaxBytesReader.
func ReadJSONLimit(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		return errors.New("nextgin: empty request body")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}
	limited := http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(limited)
	if err := dec.Decode(dst); err != nil {
		return wrapMaxBytesErr(err)
	}
	// Reject trailing JSON values or garbage after the first value.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("nextgin: unexpected trailing data in JSON body")
		}
		return wrapMaxBytesErr(err)
	}
	return nil
}

// ReadRawBody reads the full request body up to maxBytes (defaults to
// DefaultMaxBodyBytes when <= 0). Use this for payment webhooks (e.g. Stripe,
// Paystack) that require raw bytes for HMAC signature verification.
func ReadRawBody(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, error) {
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		return nil, errors.New("nextgin: empty request body")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}
	limited := http.MaxBytesReader(w, r.Body, maxBytes)
	b, err := io.ReadAll(limited)
	if err != nil {
		return nil, wrapMaxBytesErr(err)
	}
	return b, nil
}

func wrapMaxBytesErr(err error) error {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return errors.Join(ErrBodyTooLarge, err)
	}
	return err
}

// WriteJSON encodes v as JSON with the given HTTP status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes a JSON error object {"error": msg} with the given status code.
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}
