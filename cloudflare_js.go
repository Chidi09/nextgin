//go:build js && wasm

package nextgin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/syumai/workers-go"
	"github.com/syumai/workers-go/cloudflare/cron"
)

func initFrontend(_ string, _ Config, target TargetKind) (*Frontend, error) {
	if target != TargetCloudflare {
		return nil, errors.New("nextgin: only the cloudflare target is supported in js/wasm builds")
	}
	return nil, nil
}

func (a *App) runTarget(_ string) error {
	log := a.cfg.loggerOrDefault()
	timeout := a.cfg.CronTimeout
	if timeout <= 0 {
		timeout = defaultCronTimeout
	}

	cron.ScheduleTaskNonBlock(func(ctx context.Context) error {
		e, err := cron.NewEvent(ctx)
		if err != nil {
			return err
		}

		jobs := jobsForCloudflareCron(a.CronJobs(), e.Cron)
		if len(jobs) == 0 {
			log.Warn("nextgin: no cron job matches Cloudflare cron", "cron", e.Cron)
			return nil
		}

		var failed []string
		for _, job := range jobs {
			runCtx, cancel := context.WithTimeout(ctx, timeout)
			start := time.Now()
			status := a.invokeJob(runCtx, job)
			dur := time.Since(start)
			cancel()

			if status >= 400 {
				log.Error("nextgin: cron run", "name", job.Name, "status", status, "duration", dur)
				failed = append(failed, fmt.Sprintf("%s (%d)", job.Name, status))
			} else {
				log.Info("nextgin: cron run", "name", job.Name, "status", status, "duration", dur)
			}
		}

		if len(failed) > 0 {
			return fmt.Errorf("nextgin: cron job(s) failed: %s", strings.Join(failed, ", "))
		}
		return nil
	})

	workers.ServeNonBlock(a.Router)
	workers.Ready()
	select {}
}
