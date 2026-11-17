package nextgin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestBindJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("valid JSON", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"title":"hello","done":true}`))

		var body struct {
			Title string `json:"title"`
			Done  bool   `json:"done"`
		}
		if err := BindJSON(c, &body); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if body.Title != "hello" || !body.Done {
			t.Fatalf("got %+v, want {Title:hello Done:true}", body)
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"title":`))

		var body struct {
			Title string `json:"title"`
		}
		if err := BindJSON(c, &body); err == nil {
			t.Fatal("expected error for malformed JSON, got nil")
		}
	})

	t.Run("nil request", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		var body map[string]any
		if err := BindJSON(c, &body); err == nil {
			t.Fatal("expected error for nil request, got nil")
		}
	})

	t.Run("nil body", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		c.Request.Body = nil

		var body map[string]any
		if err := BindJSON(c, &body); err == nil {
			t.Fatal("expected error for nil body, got nil")
		}
	})

	t.Run("http.NoBody", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/", http.NoBody)

		var body map[string]any
		if err := BindJSON(c, &body); err == nil {
			t.Fatal("expected error for http.NoBody, got nil")
		}
	})

	t.Run("empty body", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))

		var body map[string]any
		if err := BindJSON(c, &body); err == nil {
			t.Fatal("expected error for empty body, got nil")
		}
	})
}
