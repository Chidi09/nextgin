package nextgin

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// draftModeCookie is set by Next.js draftMode().enable(); such requests must
// render dynamically.
const draftModeCookie = "__prerender_bypass"

// HandlerOptions tunes the core handler.
type HandlerOptions struct {
	// DisablePrerendered sends every page request to Next.js, even fully
	// static ones. Enable this if you call revalidatePath/revalidateTag on
	// pages that were prerendered with revalidate=false.
	DisablePrerendered bool
	// DebugHeaders adds `X-Nextgin-Source: static|prerendered|worker` to
	// responses so you can see which layer answered.
	DebugHeaders bool
}

// Handler is the engine-agnostic core: an http.Handler that serves build
// assets and fully static pages from the .nextgin output and streams
// everything else (SSR, RSC, Server Actions, ISR, middleware, redirects)
// from the Next.js workers.
//
// Mount it anywhere: net/http, chi (r.NotFound(h.ServeHTTP)), or Gin via
// App.MountFrontend / gin.WrapH.
type Handler struct {
	m        *Manifest
	assets   fs.FS
	upstream http.Handler
	opts     HandlerOptions

	static    map[string]*StaticFile
	pre       map[string]*prerendered
	nextBuild string // basePath + "/_next/static/"
}

type prerendered struct {
	entry *Prerender
	body  []byte
	etag  string
}

// NewHandler builds the core handler. assets is the .nextgin directory (or an
// embedded copy of it); upstream receives requests Go cannot answer itself,
// usually a *Pool.
func NewHandler(m *Manifest, assets fs.FS, upstream http.Handler, opts HandlerOptions) (*Handler, error) {
	h := &Handler{
		m:         m,
		assets:    assets,
		upstream:  upstream,
		opts:      opts,
		static:    make(map[string]*StaticFile, len(m.StaticFiles)),
		pre:       make(map[string]*prerendered, len(m.Prerendered)),
		nextBuild: m.BasePath + "/_next/static/",
	}
	for i := range m.StaticFiles {
		f := &m.StaticFiles[i]
		h.static[f.Pathname] = f
	}
	if !opts.DisablePrerendered {
		// Prerendered pages are small; keep them in memory with a strong ETag.
		for i := range m.Prerendered {
			e := &m.Prerendered[i]
			body, err := fs.ReadFile(assets, e.File)
			if err != nil {
				return nil, fmt.Errorf("nextgin: prerendered %s: %w", e.Pathname, err)
			}
			sum := sha256.Sum256(body)
			h.pre[e.Pathname] = &prerendered{
				entry: e,
				body:  body,
				etag:  `"` + base64.RawURLEncoding.EncodeToString(sum[:16]) + `"`,
			}
		}
	}
	return h, nil
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		// Server Actions, form posts, route handlers with bodies.
		h.forward(w, r)
		return
	}
	if isRSCRequest(r) {
		// Client navigations/prefetches: Next.js picks the right RSC payload.
		h.forward(w, r)
		return
	}

	p := r.URL.Path
	if f, ok := h.static[p]; ok {
		if strings.HasPrefix(p, h.nextBuild) {
			// Content-hashed build output: always safe to serve directly.
			h.serveStatic(w, r, f, nil)
			return
		}
		if extra, bypass := h.m.evaluate(p); !bypass {
			h.serveStatic(w, r, f, extra)
			return
		}
	}

	if pr, ok := h.pre[p]; ok && h.canServePrerendered(r) {
		if extra, bypass := h.m.evaluate(p); !bypass {
			h.servePrerendered(w, r, pr, extra)
			return
		}
	}

	h.forward(w, r)
}

func (h *Handler) source(w http.ResponseWriter, s string) {
	if h.opts.DebugHeaders {
		w.Header().Set("X-Nextgin-Source", s)
	}
}

func (h *Handler) forward(w http.ResponseWriter, r *http.Request) {
	h.source(w, "worker")
	if h.upstream == nil {
		http.NotFound(w, r)
		return
	}
	h.upstream.ServeHTTP(w, r)
}

func isRSCRequest(r *http.Request) bool {
	return r.Header.Get("RSC") == "1" ||
		r.Header.Get("Next-Action") != "" ||
		r.URL.Query().Has("_rsc")
}

func (h *Handler) canServePrerendered(r *http.Request) bool {
	if r.Header.Get("x-prerender-revalidate") != "" {
		return false // on-demand revalidation request
	}
	if _, err := r.Cookie(draftModeCookie); err == nil {
		return false
	}
	return true
}

func setHeaders(w http.ResponseWriter, hv map[string]HeaderValues, extra http.Header) {
	dst := w.Header()
	for k, vs := range hv {
		dst[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
	}
	for k, vs := range extra {
		dst[k] = vs
	}
}

func (h *Handler) serveStatic(w http.ResponseWriter, r *http.Request, f *StaticFile, extra http.Header) {
	file, err := h.assets.Open(f.File)
	if err != nil {
		h.forward(w, r) // let Next.js produce its 404 page
		return
	}
	defer file.Close()

	var modTime time.Time
	if st, err := file.Stat(); err == nil {
		modTime = st.ModTime()
	}
	setHeaders(w, f.Headers, extra)
	h.source(w, "static")
	if rs, ok := file.(io.ReadSeeker); ok {
		// Content-Type is derived from the file name when not set.
		http.ServeContent(w, r, path.Base(f.File), modTime, rs)
		return
	}
	body, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, path.Base(f.File), modTime, bytes.NewReader(body))
}

func (h *Handler) servePrerendered(w http.ResponseWriter, r *http.Request, pr *prerendered, extra http.Header) {
	setHeaders(w, pr.entry.Headers, extra)
	h.source(w, "prerendered")
	w.Header().Set("ETag", pr.etag)
	// ServeContent handles If-None-Match, Range and HEAD, and always writes
	// an explicit status (important under Gin's NoRoute, which presets 404).
	http.ServeContent(w, r, path.Base(pr.entry.File), time.Time{}, bytes.NewReader(pr.body))
}
