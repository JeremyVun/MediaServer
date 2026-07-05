import { ApiError } from '../api/client.ts'

/**
 * Extracts a user-facing message for a failed mutation: the server's
 * envelope message when the failure is an `ApiError`, otherwise a
 * caller-supplied generic fallback (e.g. "Couldn't add to collection").
 * Exported separately from `runOrToast` so it's unit-testable without a
 * `ToastProvider`.
 */
export function mutationErrorMessage(error: unknown, fallback: string): string {
  return error instanceof ApiError ? error.message : fallback
}

/**
 * A toast function shaped like `useToast().toast` — kept minimal so this
 * module doesn't depend on the Toast component.
 */
export type ToastFn = (opts: { message: string }) => void

/**
 * Runs a mutation, swallowing (and toasting) any failure so a rejected
 * promise never becomes an unhandled rejection from a user-triggered action.
 * Returns `true` on success, `false` on failure, so callers can skip
 * follow-up work (closing a dialog, chaining another mutation, etc.) when the
 * mutation didn't succeed.
 */
export async function runOrToast(
  toast: ToastFn,
  fn: () => Promise<unknown>,
  fallback: string,
): Promise<boolean> {
  try {
    await fn()
    return true
  } catch (error) {
    toast({ message: mutationErrorMessage(error, fallback) })
    return false
  }
}

/**
 * Binds a toast function so call sites read `runOrToast(fn, fallback)`
 * without re-threading `toast` through every call — the common shape once a
 * component already has `const { toast } = useToast()`.
 */
export function makeRunOrToast(toast: ToastFn) {
  return (fn: () => Promise<unknown>, fallback: string) => runOrToast(toast, fn, fallback)
}
