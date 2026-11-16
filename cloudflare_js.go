//go:build js && wasm

package nextgin

import (
	"context"
	"errors"
	"fmt"
	"syscall/js"
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
		var failed []string
		if len(jobs) == 0 {
			log.Warn("nextgin: no cron job matches Cloudflare cron", "cron", e.Cron)
		} else {
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
		}

		if msg := cronFailure(e.Cron, len(jobs), failed); msg != "" {
			log.Error("nextgin: cron event failed", "cron", e.Cron, "error", msg)
			errs := js.Global().Get(cfCronErrorsGlobal)
			if errs.IsUndefined() {
				errs = js.Global().Get("Object").New()
				js.Global().Set(cfCronErrorsGlobal, errs)
			}
			errs.Set(e.Cron, msg)
		}
		// Never return a non-nil error after cron.NewEvent succeeds: workers-go
		// panics on task errors, which would kill the persistent Go instance.
		return nil
	})

	workers.ServeNonBlock(a.Router)
	workers.Ready()
	select {}
}
