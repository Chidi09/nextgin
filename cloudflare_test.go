package nextgin

import (
	"net/http"
	"testing"
)

func TestJobsForCloudflareCron(t *testing.T) {
	noop := func(w http.ResponseWriter, r *http.Request) {}
	app := &App{Router: NewRouter()}
	app.Cron("first-weekday", "0 9 * * 1-5", noop)
	app.Cron("every-5m", "*/5 * * * *", noop)
	app.Cron("second-weekday", "0 9 * * 1-5", noop)
	app.Cron("sunday", "0 3 * * 0", noop)

	jobs := app.CronJobs()

	t.Run("matches in registration order", func(t *testing.T) {
		got := jobsForCloudflareCron(jobs, "0 9 * * 2,3,4,5,6")
		if len(got) != 2 {
			t.Fatalf("got %d jobs, want 2: %+v", len(got), got)
		}
		if got[0].Name != "first-weekday" || got[1].Name != "second-weekday" {
			t.Fatalf("got order [%s, %s], want [first-weekday, second-weekday]", got[0].Name, got[1].Name)
		}
	})

	t.Run("normalizes whitespace", func(t *testing.T) {
		got := jobsForCloudflareCron(jobs, "  */5\t *   *  * \n * ")
		if len(got) != 1 || got[0].Name != "every-5m" {
			t.Fatalf("got %+v, want [every-5m]", got)
		}
	})

	t.Run("matches sunday translation", func(t *testing.T) {
		got := jobsForCloudflareCron(jobs, "0 3 * * 1")
		if len(got) != 1 || got[0].Name != "sunday" {
			t.Fatalf("got %+v, want [sunday]", got)
		}
	})

	t.Run("no match returns empty", func(t *testing.T) {
		if got := jobsForCloudflareCron(jobs, "0 0 * * *"); len(got) != 0 {
			t.Fatalf("got %+v, want empty", got)
		}
		if got := jobsForCloudflareCron(jobs, "   "); len(got) != 0 {
			t.Fatalf("got %+v, want empty for blank expr", got)
		}
	})

	t.Run("ignores job with nil schedule", func(t *testing.T) {
		withNil := append([]CronJob{{Name: "nil-sched"}}, jobs...)
		got := jobsForCloudflareCron(withNil, "0 3 * * 1")
		if len(got) != 1 || got[0].Name != "sunday" {
			t.Fatalf("got %+v, want [sunday]", got)
		}
	})
}

func TestCronFailure(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		matched int
		failed  []string
		want    string
	}{
		{
			name:    "no matched jobs",
			expr:    "*/5 * * * *",
			matched: 0,
			want:    `nextgin: no cron job matches Cloudflare cron "*/5 * * * *"`,
		},
		{
			name:    "failed jobs",
			expr:    "0 9 * * 2,3,4,5,6",
			matched: 2,
			failed:  []string{"cleanup (500)", "sync (502)"},
			want:    "nextgin: cron job(s) failed: cleanup (500), sync (502)",
		},
		{
			name:    "all succeeded",
			expr:    "*/5 * * * *",
			matched: 2,
			want:    "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cronFailure(tc.expr, tc.matched, tc.failed)
			if got != tc.want {
				t.Fatalf("cronFailure(%q, %d, %v) = %q, want %q", tc.expr, tc.matched, tc.failed, got, tc.want)
			}
		})
	}
}
