export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    public fields?: Record<string, string>,
  ) {
    super(message)
  }
}

const PUBLIC_BASE = process.env.NEXT_PUBLIC_NEXTGIN_API_URL ?? ''

/** Browser client for Go API endpoints (sends HttpOnly cookies automatically). */
export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${PUBLIC_BASE}${path}`, {
    ...init,
    credentials: 'include',
    headers: {
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...init?.headers,
    },
  })
  return parseResponse<T>(res)
}

/** Server Component / Server Action client for calling Go directly over loopback. */
export async function internalFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const base = process.env.NEXTGIN_INTERNAL_URL || 'http://127.0.0.1:8080'
  const res = await fetch(`${base}${path}`, {
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
