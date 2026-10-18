const words = (s: string) => s.toLowerCase().split(/[^\p{L}\p{N}]+/u).filter((w) => w.length > 1)

/** Filters by the search text, then lists shows sharing a word with `near`
    first (`Star Wars Andor` puts Andor on top), keeping title order. */
export function rankShows<T extends { title: string }>(shows: T[], query: string, near: string): T[] {
  const q = query.trim().toLowerCase()
  const nearWords = new Set(words(near))
  const shared = (title: string) => words(title).some((w) => nearWords.has(w))
  const hits = q ? shows.filter((s) => s.title.toLowerCase().includes(q)) : shows
  return [...hits.filter((s) => shared(s.title)), ...hits.filter((s) => !shared(s.title))]
}
