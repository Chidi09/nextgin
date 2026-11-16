package nextgin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"regexp"
)

// ManifestVersion is the .nextgin/manifest.json schema version this runtime
// understands. It must match MANIFEST_VERSION in @nextgin/adapter.
const ManifestVersion = 1

// ManifestFile is the manifest's path inside the .nextgin directory.
const ManifestFile = "manifest.json"

// HeaderValues accepts either a JSON string or an array of strings.
type HeaderValues []string

func (h *HeaderValues) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*h = HeaderValues{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*h = many
	return nil
}

// StaticFile is a file Go serves directly (/_next/static/*, public/*).
type StaticFile struct {
	Pathname  string                  `json:"pathname"`
	File      string                  `json:"file"`
	Immutable bool                    `json:"immutable"`
	Headers   map[string]HeaderValues `json:"headers"`
}

// Prerender is a fully static (revalidate=false) page or route response.
type Prerender struct {
	Pathname string                  `json:"pathname"`
	File     string                  `json:"file"`
	Status   int                     `json:"status"`
	Headers  map[string]HeaderValues `json:"headers"`
}

// BypassRule is a Next.js routing rule (header, redirect, rewrite or
// middleware matcher) that the Go fast path must respect.
type BypassRule struct {
	Kind        string            `json:"kind"`
	Source      string            `json:"source,omitempty"`
	SourceRegex string            `json:"sourceRegex"`
	Headers     map[string]string `json:"headers,omitempty"`
	Applicable  bool              `json:"applicable"`

	re *regexp.Regexp // nil when the regex is not RE2-compatible
}

// Manifest mirrors .nextgin/manifest.json written by @nextgin/adapter.
type Manifest struct {
	Version       int    `json:"version"`
	BuildID       string `json:"buildId"`
	NextVersion   string `json:"nextVersion"`
	BasePath      string `json:"basePath"`
	TrailingSlash bool   `json:"trailingSlash"`
	Worker        struct {
		Dir   string `json:"dir"`
		Entry string `json:"entry"`
	} `json:"worker"`
	StaticFiles       []StaticFile `json:"staticFiles"`
	Prerendered       []Prerender  `json:"prerendered"`
	Bypass            []BypassRule `json:"bypass"`
	SkippedPrerenders []struct {
		Pathname string `json:"pathname"`
		Reason   string `json:"reason"`
	} `json:"skippedPrerenders"`
}

// LoadManifest reads and validates manifest.json from the given filesystem
// (typically os.DirFS(".nextgin") or an embed.FS sub-tree).
func LoadManifest(fsys fs.FS) (*Manifest, error) {
	b, err := fs.ReadFile(fsys, ManifestFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("nextgin: %s not found; run `next build` with @nextgin/adapter first", ManifestFile)
		}
		return nil, fmt.Errorf("nextgin: read manifest: %w", err)
	}
	return ParseManifest(b)
}

// ParseManifest parses manifest JSON and compiles its routing rules.
func ParseManifest(b []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("nextgin: parse manifest: %w", err)
	}
	if m.Version != ManifestVersion {
		return nil, fmt.Errorf("nextgin: manifest version %d is not supported (want %d); rebuild with a matching @nextgin/adapter", m.Version, ManifestVersion)
	}
	for i := range m.Bypass {
		// A regex Go cannot compile stays nil and is treated as "always
		// matches", which sends the request to Next.js: slower, never wrong.
		m.Bypass[i].re, _ = regexp.Compile(m.Bypass[i].SourceRegex)
	}
	return &m, nil
}

// evaluate reports which header rules apply to a path Go would serve from
// disk, and whether any rule requires Next.js to handle the request instead.
func (m *Manifest) evaluate(path string) (extra http.Header, bypass bool) {
	for i := range m.Bypass {
		r := &m.Bypass[i]
		if r.re != nil && !r.re.MatchString(path) {
			continue
		}
		if r.Kind != "headers" || !r.Applicable || r.re == nil {
			return nil, true
		}
		if extra == nil {
			extra = http.Header{}
		}
		for k, v := range r.Headers {
			extra.Set(k, v)
		}
	}
	return extra, false
}
