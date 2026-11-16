package nextgin

import (
	"fmt"
	"os"
	"runtime"

	"github.com/gin-gonic/gin"
)

// TargetKind identifies the deployment target for a nextgin application.
type TargetKind string

const (
	// TargetSelfhost runs Next.js workers behind Go with an in-process cron scheduler.
	TargetSelfhost TargetKind = "selfhost"
	// TargetVercel serves Go API routes only; Vercel handles Next.js and cron triggers.
	TargetVercel TargetKind = "vercel"
	// TargetCloudflare runs the Go backend as a Cloudflare Worker (js/wasm).
	TargetCloudflare TargetKind = "cloudflare"
)

// TargetEnv is the environment variable that explicitly selects the deploy target.
const TargetEnv = "NEXTGIN_TARGET"

// ExportEnv enables export mode when set to "1", printing route and cron metadata as JSON.
const ExportEnv = "NEXTGIN_EXPORT"

// ExportFileEnv, when set in export mode, names the file the export JSON is
// written to instead of stdout. The CLI always sets it, so that anything else
// the program prints (gin debug output, user logging) cannot corrupt the JSON.
const ExportFileEnv = "NEXTGIN_EXPORT_FILE"

// DetectTarget determines the active deployment target from environment variables
// and the build platform.
func DetectTarget() (TargetKind, error) {
	if v := os.Getenv(TargetEnv); v != "" {
		switch TargetKind(v) {
		case TargetSelfhost, TargetVercel, TargetCloudflare:
			return TargetKind(v), nil
		default:
			return "", fmt.Errorf("nextgin: unknown %s %q", TargetEnv, v)
		}
	}
	if os.Getenv("VERCEL") == "1" {
		return TargetVercel, nil
	}
	if runtime.GOOS == "js" {
		return TargetCloudflare, nil
	}
	return TargetSelfhost, nil
}

func exportMode() bool {
	return os.Getenv(ExportEnv) == "1"
}

// applyGinMode switches Gin to release mode on the vercel and cloudflare
// targets unless the GIN_MODE environment variable is set explicitly.
// Platform deploys are always production; selfhost keeps Gin's default.
func applyGinMode(target TargetKind) {
	if os.Getenv(gin.EnvGinMode) != "" {
		return
	}
	if target == TargetVercel || target == TargetCloudflare {
		gin.SetMode(gin.ReleaseMode)
	}
}
