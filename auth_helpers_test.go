package nextgin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSessionCookies(t *testing.T) {
	t.Run("secure defaults", func(t *testing.T) {
		rec := httptest.NewRecorder()
		SetSessionCookie(rec, "sid", "abc123", CookieOptions{MaxAge: 3600})
		cookies := rec.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("got %d cookies, want 1", len(cookies))
		}
		c := cookies[0]
		if c.Name != "sid" || c.Value != "abc123" || c.Path != "/" || c.MaxAge != 3600 {
			t.Fatalf("unexpected cookie: %+v", c)
		}
		if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
			t.Fatalf("expected HttpOnly=true Secure=true SameSite=Lax, got %+v", c)
		}
	})

	t.Run("split domain and clear on logout", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ClearSessionCookie(rec, "sid", CookieOptions{Domain: ".example.com"})
		cookies := rec.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("got %d cookies, want 1", len(cookies))
		}
		c := cookies[0]
		if c.Value != "" || c.MaxAge != -1 || c.Domain != "example.com" || !c.HttpOnly || !c.Secure {
			t.Fatalf("unexpected cleared cookie: %+v", c)
		}
	})
}
