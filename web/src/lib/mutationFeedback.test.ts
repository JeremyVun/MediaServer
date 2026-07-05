import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api/client.ts'
import { mutationErrorMessage, runOrToast } from './mutationFeedback.ts'

describe('mutationErrorMessage', () => {
  const cases: { name: string; error: unknown; fallback: string; want: string }[] = [
    {
      name: 'uses the server message for an ApiError',
      error: new ApiError(409, 'duplicate_name', 'A collection with this name already exists'),
      fallback: "Couldn't create collection",
      want: 'A collection with this name already exists',
    },
    {
      name: 'falls back for a plain Error (e.g. network failure)',
      error: new TypeError('Failed to fetch'),
      fallback: "Couldn't add to collection",
      want: "Couldn't add to collection",
    },
    {
      name: 'falls back for a non-Error throw',
      error: 'nope',
      fallback: "Couldn't delete",
      want: "Couldn't delete",
    },
    {
      name: 'falls back for undefined',
      error: undefined,
      fallback: "Couldn't save",
      want: "Couldn't save",
    },
  ]

  it.each(cases)('$name', ({ error, fallback, want }) => {
    expect(mutationErrorMessage(error, fallback)).toBe(want)
  })
})

describe('runOrToast', () => {
  it('resolves true and does not toast on success', async () => {
    const toast = vi.fn()
    const ok = await runOrToast(toast, async () => 'result', 'fallback')
    expect(ok).toBe(true)
    expect(toast).not.toHaveBeenCalled()
  })

  it('resolves false and toasts the ApiError message on failure', async () => {
    const toast = vi.fn()
    const ok = await runOrToast(
      toast,
      async () => {
        throw new ApiError(500, 'internal', 'Server is unavailable')
      },
      'Fallback message',
    )
    expect(ok).toBe(false)
    expect(toast).toHaveBeenCalledWith({ message: 'Server is unavailable' })
  })

  it('resolves false and toasts the fallback for a non-ApiError failure', async () => {
    const toast = vi.fn()
    const ok = await runOrToast(
      toast,
      async () => {
        throw new Error('boom')
      },
      'Fallback message',
    )
    expect(ok).toBe(false)
    expect(toast).toHaveBeenCalledWith({ message: 'Fallback message' })
  })
})
