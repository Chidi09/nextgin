package nextgin

import (
	"net/http"
	"time"
)

// CookieOptions configures SetSessionCookie and ClearSessionCookie.
type CookieOptions struct {
	// Domain scopes the cookie (e.g. ".example.com" so api.example.com and
	// example.com share auth cookies in split direct mode).
	Domain string
	// Path scopes the cookie path. Defaults to "/".
	Path string
	// MaxAge sets cookie lifetime in seconds (0 = browser session cookie).
	MaxAge int
	// SameSite defaults to http.SameSiteLaxMode.
	SameSite http.SameSite
	// Insecure disables the Secure flag. Leave false in production; set true
	// only for local http://localhost development.
	Insecure bool
}

// SetSessionCookie writes an HttpOnly session cookie with secure defaults
// (Secure=true, SameSite=Lax, Path="/").
func SetSessionCookie(w http.ResponseWriter, name, value string, opts CookieOptions) {
	http.SetCookie(w, buildSessionCookie(name, value, opts, false))
}

// ClearSessionCookie expires a session cookie on logout using matching Domain,
// Path, SameSite, and Secure attributes.
func ClearSessionCookie(w http.ResponseWriter, name string, opts CookieOptions) {
	http.SetCookie(w, buildSessionCookie(name, "", opts, true))
}

func buildSessionCookie(name, value string, opts CookieOptions, clear bool) *http.Cookie {
	path := opts.Path
	if path == "" {
		path = "/"
	}
	sameSite := opts.SameSite
	if sameSite == 0 {
		sameSite = http.SameSiteLaxMode
	}
	c := &http.Cookie{
		Name:     name,
		Value:    value,
		Domain:   opts.Domain,
		Path:     path,
		MaxAge:   opts.MaxAge,
		HttpOnly: true,
		Secure:   !opts.Insecure,
		SameSite: sameSite,
	}
	if clear {
		c.Value = ""
		c.MaxAge = -1
		c.Expires = time.Unix(0, 0).UTC()
	}
	return c
}
