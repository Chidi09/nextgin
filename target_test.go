package nextgin

import (
	"context"
	"net/http"
	"testing"
)

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
	t.Setenv(TargetEnv, string(TargetBackend))

	app, err := New("/does/not/exist", Config{Router: NewRouter()})
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
	app.Cron("heartbeat", "* * * * *", func(w http.ResponseWriter, r *http.Request) {
		ran = true
		w.WriteHeader(http.StatusOK)
	})
	if st := app.invokeJob(context.Background(), app.CronJobs()[0]); st != http.StatusOK {
		t.Fatalf("invokeJob status = %d, want 200", st)
	}
	if !ran {
		t.Fatal("expected cron handler to run")
	}
}
