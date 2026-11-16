package nextgin

import "strings"

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
