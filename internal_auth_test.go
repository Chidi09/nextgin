package nextgin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequireInternal(t *testing.T) {
	cases := []struct {
		name       string
		secret     string
		setSecret  bool
		header     string
		setHeader  bool
		wantStatus int
		wantCalled bool
	}{
		{
			name:       "right secret passes",
			secret:     "super-secret",
			setSecret:  true,
			header:     "super-secret",
			setHeader:  true,
			wantStatus: http.StatusOK,
			wantCalled: true,
		},
		{
			name:       "wrong header fails",
			secret:     "super-secret",
			setSecret:  true,
			header:     "wrong-secret",
			setHeader:  true,
			wantStatus: http.StatusUnauthorized,
			wantCalled: false,
		},
		{
			name:       "missing header fails",
			secret:     "super-secret",
			setSecret:  true,
			setHeader:  false,
			wantStatus: http.StatusUnauthorized,
			wantCalled: false,
		},
		{
			name:       "empty header fails",
			secret:     "super-secret",
			setSecret:  true,
			header:     "",
			setHeader:  true,
			wantStatus: http.StatusUnauthorized,
			wantCalled: false,
		},
		{
			name:       "secret unset fails even when header is empty",
			setSecret:  false,
			header:     "",
			setHeader:  true,
			wantStatus: http.StatusUnauthorized,
			wantCalled: false,
		},
		{
			name:       "secret unset fails when header is missing",
			setSecret:  false,
			setHeader:  false,
			wantStatus: http.StatusUnauthorized,
			wantCalled: false,
		},
		{
			name:       "secret empty fails even with non-empty header",
			secret:     "",
			setSecret:  true,
			header:     "anything",
			setHeader:  true,
			wantStatus: http.StatusUnauthorized,
			wantCalled: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setSecret {
				t.Setenv(InternalSecretEnv, tc.secret)
			} else {
				t.Setenv(InternalSecretEnv, "")
				if err := os.Unsetenv(InternalSecretEnv); err != nil {
					t.Fatal(err)
				}
			}

			r := gin.New()
			called := false
			r.GET("/internal", RequireInternal(), func(c *gin.Context) {
				called = true
				c.Status(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/internal", nil)
			if tc.setHeader {
				req.Header.Set(InternalHeader, tc.header)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if called != tc.wantCalled {
				t.Fatalf("handler called = %v, want %v", called, tc.wantCalled)
			}
			if !tc.wantCalled && rec.Body.Len() != 0 {
				t.Fatalf("expected empty body on 401, got %q", rec.Body.String())
			}
		})
	}
}
