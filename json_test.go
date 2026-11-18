package nextgin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadJSON(t *testing.T) {
	t.Run("valid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"title":"hello","done":true}`))
		var body struct {
			Title string `json:"title"`
			Done  bool   `json:"done"`
		}
		if err := ReadJSON(req, &body); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if body.Title != "hello" || !body.Done {
			t.Fatalf("got %+v, want {Title:hello Done:true}", body)
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"title":`))
		var body struct {
			Title string `json:"title"`
		}
		if err := ReadJSON(req, &body); err == nil {
			t.Fatal("expected error for malformed JSON, got nil")
		}
	})

	t.Run("nil request", func(t *testing.T) {
		var body map[string]any
		if err := ReadJSON(nil, &body); err == nil {
			t.Fatal("expected error for nil request, got nil")
		}
	})

	t.Run("nil body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Body = nil
		var body map[string]any
		if err := ReadJSON(req, &body); err == nil {
			t.Fatal("expected error for nil body, got nil")
		}
	})

	t.Run("http.NoBody", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
		var body map[string]any
		if err := ReadJSON(req, &body); err == nil {
			t.Fatal("expected error for http.NoBody, got nil")
		}
	})

	t.Run("empty body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
		var body map[string]any
		if err := ReadJSON(req, &body); err == nil {
			t.Fatal("expected error for empty body, got nil")
		}
	})
}

func TestWriteJSONAndError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusCreated, map[string]int{"id": 7})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var got map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got["id"] != 7 {
		t.Fatalf("body = %q, want id=7", rec.Body.String())
	}

	recErr := httptest.NewRecorder()
	WriteError(recErr, http.StatusBadRequest, "bad input")
	if recErr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recErr.Code)
	}
	var errBody map[string]string
	if err := json.Unmarshal(recErr.Body.Bytes(), &errBody); err != nil || errBody["error"] != "bad input" {
		t.Fatalf("body = %q, want error=bad input", recErr.Body.String())
	}
}
