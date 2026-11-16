package nextgin

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

const defaultCronTimeout = 5 * time.Minute

var (
	schedulerNow  = time.Now
	schedulerNext = func(s *Schedule, after time.Time) time.Time { return s.Next(after) }
)

func (a *App) startScheduler(ctx context.Context) func(timeout time.Duration) {
	log := a.cfg.loggerOrDefault()
	timeout := a.cfg.CronTimeout
	if timeout <= 0 {
		timeout = defaultCronTimeout
	}

	var wg sync.WaitGroup
	for _, job := range a.jobs {
		wg.Add(1)
		go func(job CronJob) {
			defer wg.Done()
			var running atomic.Bool

			for {
				now := schedulerNow()
				next := schedulerNext(job.Schedule, now)
				if next.IsZero() {
					log.Warn("nextgin: cron schedule has no upcoming run", "name", job.Name)
					return
				}

				delay := next.Sub(schedulerNow())
				if delay < 0 {
					delay = 0
				}
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}

				if !running.CompareAndSwap(false, true) {
					log.Warn("nextgin: cron run skipped, previous run still executing", "name", job.Name)
					continue
				}

				wg.Add(1)
				go func() {
					defer wg.Done()
					defer running.Store(false)

					runCtx, cancel := context.WithTimeout(context.Background(), timeout)
					defer cancel()

					start := schedulerNow()
					status := a.invokeJob(runCtx, job)
					dur := schedulerNow().Sub(start)

					if status >= 400 {
						log.Error("nextgin: cron run", "name", job.Name, "status", status, "duration", dur)
					} else {
						log.Info("nextgin: cron run", "name", job.Name, "status", status, "duration", dur)
					}
				}()
			}
		}(job)
	}

	return func(waitTimeout time.Duration) {
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		if waitTimeout <= 0 {
			<-done
			return
		}
		t := time.NewTimer(waitTimeout)
		defer t.Stop()
		select {
		case <-done:
		case <-t.C:
		}
	}
}
