import type { ApiErrorBody } from './types.ts'

/** Error carrying the server's stable machine-readable code. */
export class ApiError extends Error {
  readonly code: string
  readonly status: number

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

/**
 * Typed fetch wrapper for /api. Decodes the uniform error envelope
 * ({error: {code, message}}) into ApiError.
 */
export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers)
  if (!headers.has('Accept')) headers.set('Accept', 'application/json')
  const res = await fetch(path, { ...init, headers })
  if (!res.ok) {
    let code = 'unknown'
    let message = `${res.status} ${res.statusText}`
    try {
      const body = (await res.json()) as ApiErrorBody
      code = body.error.code
      message = body.error.message
    } catch {
      // Non-JSON error body; keep the status text.
    }
    throw new ApiError(res.status, code, message)
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

export function jsonApi<T>(path: string, method: string, body: unknown): Promise<T> {
  return api<T>(path, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}
