import fs from 'node:fs'
import path from 'node:path'

export type InitLayout = 'selfhost' | 'split'

export interface InitOptions {
  targetDir: string
  name?: string
  modulePath?: string
  layout?: InitLayout
  force?: boolean
  nextginVersion?: string
  adapterVersion?: string
}

export interface InitResult {
  targetDir: string
  name: string
  modulePath: string
  layout: InitLayout
  files: string[]
}

const VALID_LAYOUTS = new Set<InitLayout>(['selfhost', 'split'])

export function normalizeAppName(raw: string): string {
  const cleaned = raw
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, '-')
    .replace(/^-+|-+$/g, '')
  return cleaned || 'nextgin-app'
}

export function validateInitOptions(opts: InitOptions): {
  targetDir: string
  name: string
  modulePath: string
  layout: InitLayout
  force: boolean
  nextginVersion: string
  adapterVersion: string
} {
  const targetDir = path.resolve(opts.targetDir)
  const layout = opts.layout ?? 'selfhost'
  if (!VALID_LAYOUTS.has(layout)) {
    throw new Error(`invalid --layout "${layout}": expected "selfhost" or "split"`)
  }
  const baseName = path.basename(targetDir)
  const name = normalizeAppName(opts.name ?? baseName)
  const modulePath = (opts.modulePath ?? `example.com/${name}`).trim()
  if (!modulePath || /\s/.test(modulePath)) {
    throw new Error(`invalid --module "${modulePath}": must be a valid Go module path`)
  }
  return {
    targetDir,
    name,
    modulePath,
    layout,
    force: opts.force === true,
    nextginVersion: opts.nextginVersion ?? 'v0.1.0',
    adapterVersion: opts.adapterVersion ?? '^0.1.0',
  }
}

function assertDirWritable(dir: string, force: boolean) {
  if (!fs.existsSync(dir)) return
  const stat = fs.statSync(dir)
  if (!stat.isDirectory()) {
    throw new Error(`target path exists and is not a directory: ${dir}`)
  }
  const entries = fs.readdirSync(dir).filter((e) => e !== '.git')
  if (entries.length > 0 && !force) {
    throw new Error(`target directory is not empty: ${dir} (pass --force to overwrite)`)
  }
}

export function scaffoldProject(rawOpts: InitOptions): InitResult {
  const opts = validateInitOptions(rawOpts)
  assertDirWritable(opts.targetDir, opts.force)

  const files = opts.layout === 'split' ? buildSplitFiles(opts) : buildSelfhostFiles(opts)
  const written: string[] = []

  for (const [relPath, content] of Object.entries(files)) {
    const fullPath = path.join(opts.targetDir, relPath)
    fs.mkdirSync(path.dirname(fullPath), { recursive: true })
    fs.writeFileSync(fullPath, content, 'utf8')
    written.push(relPath)
  }

  return {
    targetDir: opts.targetDir,
    name: opts.name,
    modulePath: opts.modulePath,
    layout: opts.layout,
    files: written.sort(),
  }
}

function buildSelfhostFiles(opts: ReturnType<typeof validateInitOptions>): Record<string, string> {
  return {
    '.gitignore': gitignoreContent(),
    'AGENTS.md': projectAgentsMd(opts.name, 'selfhost'),
    'go.mod': goModContent(opts.modulePath, opts.nextginVersion),
    'main.go': mainGoContent(opts.modulePath, false),
    'internal/domain/errors.go': domainErrorsGo(),
    'internal/domain/models.go': domainModelsGo(),
    'internal/store/memory.go': storeMemoryGo(opts.modulePath),
    'internal/service/item_service.go': serviceGo(opts.modulePath),
    'internal/httpapi/respond.go': httpRespondGo(opts.modulePath),
    'internal/httpapi/handlers.go': httpHandlersGo(opts.modulePath),
    'internal/httpapi/handlers_test.go': httpHandlersTestGo(opts.modulePath),
    'package.json': packageJsonContent(opts.name, opts.adapterVersion, false),
    'next.config.mjs': nextConfigContent(false),
    'tsconfig.json': tsconfigContent(),
    'lib/api.ts': libApiTs(),
    'app/globals.css': globalsCss(),
    'app/layout.tsx': layoutTsx(opts.name),
    'app/page.tsx': pageTsx(opts.name),
  }
}

function buildSplitFiles(opts: ReturnType<typeof validateInitOptions>): Record<string, string> {
  return {
    '.gitignore': gitignoreContent(),
    'AGENTS.md': projectAgentsMd(opts.name, 'split'),
    'backend/go.mod': goModContent(opts.modulePath, opts.nextginVersion),
    'backend/cmd/server/main.go': mainGoContent(opts.modulePath, true),
    'backend/internal/domain/errors.go': domainErrorsGo(),
    'backend/internal/domain/models.go': domainModelsGo(),
    'backend/internal/store/memory.go': storeMemoryGo(opts.modulePath),
    'backend/internal/service/item_service.go': serviceGo(opts.modulePath),
    'backend/internal/httpapi/respond.go': httpRespondGo(opts.modulePath),
    'backend/internal/httpapi/handlers.go': httpHandlersGo(opts.modulePath),
    'backend/internal/httpapi/handlers_test.go': httpHandlersTestGo(opts.modulePath),
    'web/package.json': packageJsonContent(opts.name, opts.adapterVersion, true),
    'web/next.config.mjs': nextConfigContent(true),
    'web/tsconfig.json': tsconfigContent(),
    'web/lib/api.ts': libApiTs(),
    'web/app/globals.css': globalsCss(),
    'web/app/layout.tsx': layoutTsx(opts.name),
    'web/app/page.tsx': pageTsx(opts.name),
  }
}

function gitignoreContent(): string {
  return `node_modules/
.next/
.nextgin/
server
*.db
*.db-wal
*.db-shm
.env
.env.local
tsconfig.tsbuildinfo
`
}

function projectAgentsMd(name: string, layout: InitLayout): string {
  const goRoot = layout === 'split' ? 'backend/' : ''
  const webRoot = layout === 'split' ? 'web/' : ''
  return `# AGENTS.md — ${name}

This app is built on **nextgin** (Next.js App Router + pure-Go \`net/http\` API & cron backend).

## Architecture Rules

1. **Keep \`main.go\` thin (wiring only).**
   - \`${goRoot}internal/domain/\`: shared types & sentinel errors.
   - \`${goRoot}internal/store/\`: database/storage queries; translate \`sql.ErrNoRows\` into \`domain.ErrNotFound\`.
   - \`${goRoot}internal/service/\`: business logic, authorization checks, and transaction boundaries (no \`net/http\` imports).
   - \`${goRoot}internal/httpapi/\`: HTTP handlers, request DTOs with \`Validate()\`, middleware, and centralized error mapping in \`respond.go\`.
2. **Go owns hot API routes & crons.**
   - Put auth, sessions, payments, webhooks, rate limiting, and cron jobs in Go.
   - Always pass \`r.Context()\` down through service and store calls.
   - Always read payment webhooks with \`nextgin.ReadRawBody\` before verifying HMAC signatures.
3. **Next.js owns UI, SSR, and RSC.**
   - Centralize API calls in \`${webRoot}lib/api.ts\` (\`apiFetch\` for browser, \`internalFetch\` for Server Components).
`
}

function goModContent(modulePath: string, nextginVersion: string): string {
  return `module ${modulePath}

go 1.24

require github.com/Chidi09/nextgin ${nextginVersion}
`
}

function mainGoContent(modulePath: string, split: boolean): string {
  const corsBlock = split
    ? `
	if origin := os.Getenv("CORS_ORIGIN"); origin != "" {
		app.Router.Use(nextgin.CORS(origin))
	}
`
    : ''
  const newCall = split
    ? `app, err := nextgin.New("", nextgin.Config{
		Port: port,
	})`
    : `app, err := nextgin.New("./.nextgin", nextgin.Config{
		Workers: 2,
		Runtime: envOr("NEXTGIN_RUNTIME", "node"),
		Port:    port,
	})`

  return `package main

import (
	"log"
	"net/http"
	"os"

	"github.com/Chidi09/nextgin"

	"${modulePath}/internal/httpapi"
	"${modulePath}/internal/service"
	"${modulePath}/internal/store"
)

func main() {
	port := envOr("PORT", "8080")

	repo := store.NewMemoryStore()
	svc := service.NewItemService(repo)

	${newCall}
	if err != nil {
		log.Fatal(err)
	}
${corsBlock}
	// Example scheduled job (respects r.Context() on SIGTERM).
	app.Cron("heartbeat", "*/5 * * * *", func(w http.ResponseWriter, r *http.Request) {
		if err := r.Context().Err(); err != nil {
			nextgin.WriteError(w, http.StatusServiceUnavailable, "shutting down")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	httpapi.NewHandlers(svc).Register(app.Router.Group("/api"))

	app.MountFrontend()
	if err := app.Run(":" + port); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
`
}

function domainErrorsGo(): string {
  return `package domain

import "errors"

var (
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrUnauthorized = errors.New("unauthorized")
)

type ValidationError struct {
	Fields map[string]string
}

func (e ValidationError) Error() string { return "validation failed" }
`
}

function domainModelsGo(): string {
  return `package domain

import "time"

type Item struct {
	ID        string    \`json:"id"\`
	Title     string    \`json:"title"\`
	CreatedAt time.Time \`json:"createdAt"\`
}
`
}

function storeMemoryGo(modulePath: string): string {
  return `package store

import (
	"context"
	"sync"

	"${modulePath}/internal/domain"
)

// MemoryStore is a starter repository. Swap it for SQLite/Postgres (e.g. sqlc)
// while keeping the service.ItemRepository interface unchanged.
type MemoryStore struct {
	mu    sync.RWMutex
	items []domain.Item
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: []domain.Item{}}
}

func (s *MemoryStore) List(ctx context.Context) ([]domain.Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Item, len(s.items))
	copy(out, s.items)
	return out, nil
}

func (s *MemoryStore) Create(ctx context.Context, item domain.Item) (domain.Item, error) {
	if err := ctx.Err(); err != nil {
		return domain.Item{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, item)
	return item, nil
}
`
}

function serviceGo(modulePath: string): string {
  return `package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"

	"${modulePath}/internal/domain"
)

type ItemRepository interface {
	List(ctx context.Context) ([]domain.Item, error)
	Create(ctx context.Context, item domain.Item) (domain.Item, error)
}

type ItemService struct {
	repo ItemRepository
}

func NewItemService(repo ItemRepository) *ItemService {
	return &ItemService{repo: repo}
}

func (s *ItemService) List(ctx context.Context) ([]domain.Item, error) {
	return s.repo.List(ctx)
}

func (s *ItemService) Create(ctx context.Context, title string) (domain.Item, error) {
	item := domain.Item{
		ID:        newID(),
		Title:     strings.TrimSpace(title),
		CreatedAt: time.Now().UTC(),
	}
	return s.repo.Create(ctx, item)
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
`
}

function httpRespondGo(modulePath: string): string {
  return `package httpapi

import (
	"errors"
	"log"
	"net/http"

	"github.com/Chidi09/nextgin"

	"${modulePath}/internal/domain"
)

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

func handle(fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			writeDomainError(w, r, err)
		}
	}
}

func writeDomainError(w http.ResponseWriter, r *http.Request, err error) {
	var valErr domain.ValidationError
	switch {
	case errors.As(err, &valErr):
		nextgin.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":  "validation failed",
			"fields": valErr.Fields,
		})
	case errors.Is(err, nextgin.ErrBodyTooLarge):
		nextgin.WriteError(w, http.StatusRequestEntityTooLarge, "request body too large")
	case errors.Is(err, domain.ErrNotFound):
		nextgin.WriteError(w, http.StatusNotFound, "resource not found")
	case errors.Is(err, domain.ErrConflict):
		nextgin.WriteError(w, http.StatusConflict, "resource already exists")
	case errors.Is(err, domain.ErrUnauthorized):
		nextgin.WriteError(w, http.StatusUnauthorized, "unauthorized")
	default:
		log.Printf("http error %s %s: %v", r.Method, r.URL.Path, err)
		nextgin.WriteError(w, http.StatusInternalServerError, "internal server error")
	}
}
`
}

function httpHandlersGo(modulePath: string): string {
  return `package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Chidi09/nextgin"

	"${modulePath}/internal/domain"
	"${modulePath}/internal/service"
)

type Handlers struct {
	items *service.ItemService
}

func NewHandlers(items *service.ItemService) *Handlers {
	return &Handlers{items: items}
}

func (h *Handlers) Register(api *nextgin.Router) {
	api.GET("/health", handle(h.Health))
	api.GET("/items", handle(h.ListItems))
	api.POST("/items", handle(h.CreateItem))
}

func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) error {
	nextgin.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	return nil
}

func (h *Handlers) ListItems(w http.ResponseWriter, r *http.Request) error {
	items, err := h.items.List(r.Context())
	if err != nil {
		return err
	}
	nextgin.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

type createItemRequest struct {
	Title string \`json:"title"\`
}

func (req createItemRequest) Validate() error {
	if strings.TrimSpace(req.Title) == "" {
		return domain.ValidationError{Fields: map[string]string{"title": "required"}}
	}
	return nil
}

func (h *Handlers) CreateItem(w http.ResponseWriter, r *http.Request) error {
	var req createItemRequest
	if err := nextgin.ReadJSON(r, &req); err != nil {
		if errors.Is(err, nextgin.ErrBodyTooLarge) {
			return err
		}
		return domain.ValidationError{Fields: map[string]string{"body": "invalid JSON"}}
	}
	if err := req.Validate(); err != nil {
		return err
	}
	item, err := h.items.Create(r.Context(), req.Title)
	if err != nil {
		return err
	}
	nextgin.WriteJSON(w, http.StatusCreated, item)
	return nil
}
`
}

function httpHandlersTestGo(modulePath: string): string {
  return `package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Chidi09/nextgin"

	"${modulePath}/internal/httpapi"
	"${modulePath}/internal/service"
	"${modulePath}/internal/store"
)

func TestItemsAPI(t *testing.T) {
	t.Setenv("NEXTGIN_TARGET", "backend")
	app, err := nextgin.New("", nextgin.Config{})
	if err != nil {
		t.Fatal(err)
	}
	svc := service.NewItemService(store.NewMemoryStore())
	httpapi.NewHandlers(svc).Register(app.Router.Group("/api"))

	// Validation error returns 422.
	rec := httptest.NewRecorder()
	app.Router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/items", strings.NewReader(\`{"title":""}\`)))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422", rec.Code)
	}

	// Valid create returns 201.
	rec = httptest.NewRecorder()
	app.Router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/items", strings.NewReader(\`{"title":"Ship fast"}\`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201: %s", rec.Code, rec.Body.String())
	}
}
`
}

function packageJsonContent(name: string, adapterVersion: string, split: boolean): string {
  const scripts = split
    ? {
        dev: 'next dev',
        build: 'next build',
        start: 'next start',
      }
    : {
        dev: 'nextgin dev',
        build: 'nextgin build',
        start: './server',
      }
  return (
    JSON.stringify(
      {
        name,
        private: true,
        scripts,
        dependencies: {
          '@nextgin/adapter': adapterVersion,
          next: '^16.0.0',
          react: '^19.0.0',
          'react-dom': '^19.0.0',
        },
        devDependencies: {
          '@types/node': '^22.0.0',
          '@types/react': '^19.0.0',
          typescript: '^5.7.0',
        },
      },
      null,
      2,
    ) + '\n'
  )
}

function nextConfigContent(split: boolean): string {
  if (split) {
    return `/** @type {import('next').NextConfig} */
export default {}
`
  }
  return `import { createNextGinAdapter } from '@nextgin/adapter'

/** @type {import('next').NextConfig} */
export default {
  adapterPath: createNextGinAdapter(),
}
`
}

function tsconfigContent(): string {
  return (
    JSON.stringify(
      {
        compilerOptions: {
          target: 'ES2022',
          lib: ['dom', 'dom.iterable', 'esnext'],
          allowJs: true,
          skipLibCheck: true,
          strict: true,
          noEmit: true,
          esModuleInterop: true,
          module: 'esnext',
          moduleResolution: 'bundler',
          resolveJsonModule: true,
          isolatedModules: true,
          jsx: 'react-jsx',
          paths: { '@/*': ['./*'] },
          plugins: [{ name: 'next' }],
        },
        include: ['next-env.d.ts', '**/*.ts', '**/*.tsx', '.next/types/**/*.ts'],
        exclude: ['node_modules', '.nextgin'],
      },
      null,
      2,
    ) + '\n'
  )
}

function libApiTs(): string {
  return `export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    public fields?: Record<string, string>,
  ) {
    super(message)
  }
}

const PUBLIC_BASE = process.env.NEXT_PUBLIC_NEXTGIN_API_URL ?? ''

/** Use in Client Components (browser). Sends HttpOnly cookies automatically. */
export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(\`\${PUBLIC_BASE}\${path}\`, {
    ...init,
    credentials: 'include',
    headers: {
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...init?.headers,
    },
  })
  return parseResponse<T>(res)
}

/** Use in Server Components & Server Actions only. Calls Go directly over loopback. */
export async function internalFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const base = process.env.NEXTGIN_INTERNAL_URL || 'http://127.0.0.1:8080'
  const res = await fetch(\`\${base}\${path}\`, {
    ...init,
    cache: init?.cache ?? 'no-store',
    headers: {
      ...(process.env.NEXTGIN_INTERNAL_SECRET
        ? { 'X-Nextgin-Internal': process.env.NEXTGIN_INTERNAL_SECRET }
        : {}),
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...init?.headers,
    },
  })
  return parseResponse<T>(res)
}

async function parseResponse<T>(res: Response): Promise<T> {
  if (res.status === 204) return undefined as T
  const data = await res.json().catch(() => ({}))
  if (!res.ok) {
    throw new ApiError(res.status, data.error ?? res.statusText, data.fields)
  }
  return data as T
}
`
}

function globalsCss(): string {
  return `* { box-sizing: border-box; margin: 0; padding: 0; }
body {
  font-family: system-ui, -apple-system, sans-serif;
  background: #0b0d12;
  color: #f3f5f9;
  line-height: 1.5;
  padding: 48px 24px;
}
main { max-width: 720px; margin: 0 auto; }
h1 { font-size: 2rem; margin-bottom: 12px; }
p { color: #a1a8b8; }
code { background: #161a24; padding: 2px 6px; border-radius: 4px; }
`
}

function layoutTsx(name: string): string {
  return `import type { Metadata } from 'next'
import './globals.css'

export const metadata: Metadata = {
  title: '${name}',
  description: 'Built with nextgin (Next.js + pure Go)',
}

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  )
}
`
}

function pageTsx(name: string): string {
  return `export default function HomePage() {
  return (
    <main>
      <h1>${name}</h1>
      <p>
        Next.js App Router frontend powered by a pure-Go <code>nextgin</code> backend.
        Check <code>/api/health</code> or <code>/_nextgin/health</code>.
      </p>
    </main>
  )
}
`
}
