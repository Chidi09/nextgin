package nextgin

import (
	"cmp"
	"encoding/json"
	"io"
	"slices"
	"strings"
)

// ExportVersion is the schema version emitted by App.Export.
const ExportVersion = 1

// ExportRoute describes a single HTTP route registered on the Router.
type ExportRoute struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// ExportCron describes a registered cron job in the export payload.
type ExportCron struct {
	Name          string `json:"name"`
	Schedule      string `json:"schedule"`
	Cloudflare    string `json:"cloudflare"`
	Path          string `json:"path"`
	RunsPerDayMax int    `json:"runsPerDayMax"`
}

// Export is the JSON-serializable metadata describing an App's routes and cron jobs.
type Export struct {
	Version int           `json:"version"`
	Routes  []ExportRoute `json:"routes"`
	Crons   []ExportCron  `json:"crons"`
}

// Export returns the App's registered routes (excluding /_nextgin/ internal routes)
// and cron jobs for consumption by the nextgin CLI.
func (a *App) Export() Export {
	routes := []ExportRoute{}
	if a.Router != nil {
		for _, r := range a.Router.Routes() {
			if strings.HasPrefix(r.Path, "/_nextgin/") {
				continue
			}
			routes = append(routes, ExportRoute{
				Method: r.Method,
				Path:   r.Path,
			})
		}
	}
	slices.SortFunc(routes, func(x, y ExportRoute) int {
		if c := cmp.Compare(x.Path, y.Path); c != 0 {
			return c
		}
		return cmp.Compare(x.Method, y.Method)
	})

	crons := make([]ExportCron, 0, len(a.jobs))
	for _, j := range a.jobs {
		crons = append(crons, ExportCron{
			Name:          j.Name,
			Schedule:      j.Schedule.String(),
			Cloudflare:    j.Schedule.CloudflareExpr(),
			Path:          j.Path,
			RunsPerDayMax: j.Schedule.RunsPerDayMax(),
		})
	}

	return Export{
		Version: ExportVersion,
		Routes:  routes,
		Crons:   crons,
	}
}

func (a *App) writeExport(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(a.Export())
}
