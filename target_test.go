package nextgin

import (
	"os"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestApplyGinMode(t *testing.T) {
	t.Cleanup(func() {
		gin.SetMode(gin.DebugMode)
	})

	cases := []struct {
		name     string
		target   TargetKind
		ginMode  string
		setEnv   bool
		wantMode string
	}{
		{
			name:     "vercel with unset GIN_MODE",
			target:   TargetVercel,
			wantMode: gin.ReleaseMode,
		},
		{
			name:     "cloudflare with unset GIN_MODE",
			target:   TargetCloudflare,
			wantMode: gin.ReleaseMode,
		},
		{
			name:     "selfhost with unset GIN_MODE",
			target:   TargetSelfhost,
			wantMode: gin.DebugMode,
		},
		{
			name:     "vercel with GIN_MODE=debug",
			target:   TargetVercel,
			ginMode:  gin.DebugMode,
			setEnv:   true,
			wantMode: gin.DebugMode,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.DebugMode)
			t.Cleanup(func() {
				gin.SetMode(gin.DebugMode)
			})
			if tc.setEnv {
				t.Setenv(gin.EnvGinMode, tc.ginMode)
			} else {
				t.Setenv(gin.EnvGinMode, "")
				if err := os.Unsetenv(gin.EnvGinMode); err != nil {
					t.Fatal(err)
				}
			}
			applyGinMode(tc.target)
			if got := gin.Mode(); got != tc.wantMode {
				t.Fatalf("gin.Mode() = %q, want %q", got, tc.wantMode)
			}
		})
	}
}
