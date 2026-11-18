package nextgin

import (
	"fmt"
	"os"
)

// TargetKind identifies the deployment target for a nextgin application.
type TargetKind string

const (
	// TargetSelfhost runs Next.js workers behind Go with an in-process cron scheduler.
	TargetSelfhost TargetKind = "selfhost"
	// TargetVercel serves Go API routes only; Vercel handles Next.js and cron triggers.
	TargetVercel TargetKind = "vercel"
	// TargetBackend runs an API-only Go server with the in-process cron scheduler and no Next.js frontend; the web app is deployed elsewhere.
	TargetBackend TargetKind = "backend"
)

// TargetEnv is the environment variable that explicitly selects the deploy target.
const TargetEnv = "NEXTGIN_TARGET"

// SchedulerEnv disables the in-process cron scheduler on selfhost/backend when set to "off".
// Replicas of a backend set NEXTGIN_SCHEDULER=off so jobs fire once.
const SchedulerEnv = "NEXTGIN_SCHEDULER"

// ExportEnv enables export mode when set to "1", printing route and cron metadata as JSON.
const ExportEnv = "NEXTGIN_EXPORT"

// ExportFileEnv, when set in export mode, names the file the export JSON is
// written to instead of stdout. The CLI always sets it, so that anything else
// the program prints cannot corrupt the JSON.
const ExportFileEnv = "NEXTGIN_EXPORT_FILE"

// DetectTarget determines the active deployment target from environment variables.
func DetectTarget() (TargetKind, error) {
	if v := os.Getenv(TargetEnv); v != "" {
		switch TargetKind(v) {
		case TargetSelfhost, TargetVercel, TargetBackend:
			return TargetKind(v), nil
		default:
			return "", fmt.Errorf("nextgin: unknown %s %q", TargetEnv, v)
		}
	}
	if os.Getenv("VERCEL") == "1" {
		return TargetVercel, nil
	}
	return TargetSelfhost, nil
}

func exportMode() bool {
	return os.Getenv(ExportEnv) == "1"
}

func (a *App) schedulerEnabled() bool {
	if a.target != TargetSelfhost && a.target != TargetBackend {
		return false
	}
	return os.Getenv(SchedulerEnv) != "off"
}
