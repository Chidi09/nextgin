package nextgin

import (
	"testing"

	"github.com/gin-gonic/gin"
)

func TestJobsForCloudflareCron(t *testing.T) {
	app := &App{Router: gin.New()}
	app.Cron("first-weekday", "0 9 * * 1-5", func(c *gin.Context) {})
	app.Cron("every-5m", "*/5 * * * *", func(c *gin.Context) {})
	app.Cron("second-weekday", "0 9 * * 1-5", func(c *gin.Context) {})
	app.Cron("sunday", "0 3 * * 0", func(c *gin.Context) {})

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
