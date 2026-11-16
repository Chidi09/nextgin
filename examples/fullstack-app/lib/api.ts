// Server-side helper for calling the Go API from Server Components / Actions.
// nextgin sets NEXTGIN_INTERNAL_URL for its workers (and for `nextgin dev`).
export function apiURL(path: string): string {
  const base = process.env.NEXTGIN_INTERNAL_URL ?? 'http://127.0.0.1:8080'
  return new URL(path, base).toString()
}

export type Todo = { id: number; title: string; done: boolean }
