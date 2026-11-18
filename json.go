package nextgin

import (
	"encoding/json"
	"errors"
	"net/http"
)

// ReadJSON decodes the JSON request body into dst using encoding/json.
func ReadJSON(r *http.Request, dst any) error {
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		return errors.New("nextgin: empty request body")
	}
	return json.NewDecoder(r.Body).Decode(dst)
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
