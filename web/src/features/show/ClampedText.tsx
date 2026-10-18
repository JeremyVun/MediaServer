import { useState } from 'react'

/** A summary clamped to 3 lines; a tap toggles the rest. */
export function ClampedText({ text, className = '' }: { text: string | null; className?: string }) {
  const [open, setOpen] = useState(false)
  if (!text) return null
  return (
    <button
      type="button"
      aria-expanded={open}
      onClick={() => setOpen((v) => !v)}
      className={`text-secondary block max-w-2xl cursor-pointer text-left text-base ${className}`}
    >
      <span className={`whitespace-pre-line ${open ? '' : 'line-clamp-3'}`}>{text}</span>
    </button>
  )
}
