package nextgin

import (
	"context"
	"net/http"
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
			name:     "backend with unset GIN_MODE",
			target:   TargetBackend,
			wantMode: gin.ReleaseMode,
		},
		{
			name:     "selfhost with unset GIN_MODE",
			target:   TargetSelfhost,
			wantMode: gin.DebugMode,
		},
		{
			name:     "backend with GIN_MODE=debug",
			target:   TargetBackend,
			ginMode:  gin.DebugMode,
			setEnv:   true,
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

func TestSchedulerEnabled(t *testing.T) {
	cases := []struct {
		name         string
		target       TargetKind
		schedulerEnv string
		want         bool
	}{
		{name: "selfhost default", target: TargetSelfhost, want: true},
		{name: "selfhost off", target: TargetSelfhost, schedulerEnv: "off", want: false},
		{name: "selfhost non-off", target: TargetSelfhost, schedulerEnv: "on", want: true},
		{name: "backend default", target: TargetBackend, want: true},
		{name: "backend off", target: TargetBackend, schedulerEnv: "off", want: false},
		{name: "backend non-off", target: TargetBackend, schedulerEnv: "false", want: true},
		{name: "vercel default", target: TargetVercel, want: false},
		{name: "vercel off", target: TargetVercel, schedulerEnv: "off", want: false},
		{name: "cloudflare default", target: TargetCloudflare, want: false},
		{name: "cloudflare off", target: TargetCloudflare, schedulerEnv: "off", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(SchedulerEnv, tc.schedulerEnv)
			app := &App{target: tc.target}
			if got := app.schedulerEnabled(); got != tc.want {
				t.Fatalf("schedulerEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBackendTargetSkipsFrontendAndRunsCron(t *testing.T) {
	t.Cleanup(func() {
		gin.SetMode(gin.DebugMode)
	})
	t.Setenv(TargetEnv, string(TargetBackend))
	t.Setenv(gin.EnvGinMode, "")
	if err := os.Unsetenv(gin.EnvGinMode); err != nil {
		t.Fatal(err)
	}

	app, err := New("/does/not/exist", Config{Router: gin.New()})
	if err != nil {
		t.Fatalf("New on backend target failed: %v", err)
	}
	if app.Frontend != nil {
		t.Fatal("expected nil Frontend on backend target")
	}
	app.MountFrontend()
	if err := app.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	var ran bool
	app.Cron("heartbeat", "* * * * *", func(c *gin.Context) {
		ran = true
		c.Status(http.StatusOK)
	})
	if st := app.invokeJob(context.Background(), app.CronJobs()[0]); st != http.StatusOK {
		t.Fatalf("invokeJob status = %d, want 200", st)
	}
	if !ran {
		t.Fatal("expected cron handler to run")
	}
}
