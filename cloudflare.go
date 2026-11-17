package nextgin

import (
	"fmt"
	"strings"
)

const cfCronErrorsGlobal = "__nextginCronErrors"

// cronFailure returns the failure message for one Cloudflare cron event, or
// "" when every matched job succeeded. failed holds "name (status)" entries.
func cronFailure(expr string, matched int, failed []string) string {
	if matched == 0 {
		return fmt.Sprintf("nextgin: no cron job matches Cloudflare cron %q", expr)
	}
	if len(failed) > 0 {
		return "nextgin: cron job(s) failed: " + strings.Join(failed, ", ")
	}
	return ""
}

func jobsForCloudflareCron(jobs []CronJob, expr string) []CronJob {
	norm := strings.Join(strings.Fields(expr), " ")
	var matched []CronJob
	for _, job := range jobs {
		if job.Schedule != nil && job.Schedule.CloudflareExpr() == norm {
			matched = append(matched, job)
		}
	}
	return matched
}
