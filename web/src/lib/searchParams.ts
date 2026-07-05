// Pure helpers for URL search-param state (library filters, watch resume
// override). Kept free of React so they're table-testable; the pages wire
// these to useSearchParams.

export type LibrarySort = 'added' | 'title' | 'year'

export const DEFAULT_LIBRARY_SORT: LibrarySort = 'added'

const VALID_SORTS: LibrarySort[] = ['added', 'title', 'year']

/** Sentinel `collection` value meaning "items in no collection". */
export const UNCOLLECTED = 'none'

export interface LibraryUrlState {
  q: string
  sort: LibrarySort
  collectionID?: number
  // True when filtering to items in no collection. Mutually exclusive with
  // collectionID — both share the `collection` URL param.
  uncollected: boolean
}

/** Reads library filter state from URL search params, falling back to
 * defaults for missing/invalid values. */
export function parseLibraryParams(params: URLSearchParams): LibraryUrlState {
  const q = params.get('q') ?? ''
  const sortRaw = params.get('sort')
  const sort = isLibrarySort(sortRaw) ? sortRaw : DEFAULT_LIBRARY_SORT
  const collectionRaw = params.get('collection')
  const uncollected = collectionRaw === UNCOLLECTED
  const collectionID =
    collectionRaw && /^\d+$/.test(collectionRaw) ? Number(collectionRaw) : undefined
  return { q, sort, collectionID, uncollected }
}

function isLibrarySort(value: string | null): value is LibrarySort {
  return VALID_SORTS.includes(value as LibrarySort)
}

/** Builds the param updates for a library URL state, omitting defaults
 * (empty q, default sort, no collection) so the URL stays clean — the
 * caller deletes any key whose update value is undefined. */
export function libraryParamUpdates(state: LibraryUrlState): Record<string, string | undefined> {
  return {
    q: state.q ? state.q : undefined,
    sort: state.sort !== DEFAULT_LIBRARY_SORT ? state.sort : undefined,
    collection: state.uncollected
      ? UNCOLLECTED
      : state.collectionID != null
        ? String(state.collectionID)
        : undefined,
  }
}

/** Applies a set of key -> value-or-undefined updates onto existing
 * URLSearchParams, deleting keys whose update is undefined/null. Returns a
 * new URLSearchParams, leaving the input untouched. */
export function withParamUpdates(
  params: URLSearchParams,
  updates: Record<string, string | undefined | null>,
): URLSearchParams {
  const next = new URLSearchParams(params)
  for (const [key, value] of Object.entries(updates)) {
    if (value == null) next.delete(key)
    else next.set(key, value)
  }
  return next
}

/** Appends a search param to a path (which may already have a query
 * string), e.g. for the watch URL's start-from-zero override. */
export function withSearchParam(path: string, key: string, value: string): string {
  const separator = path.includes('?') ? '&' : '?'
  return `${path}${separator}${key}=${encodeURIComponent(value)}`
}

/** Parses the player's `t` resume-override search param: a valid,
 * non-negative number overrides the stored-progress resume position; any
 * other value (missing, negative, non-numeric) means "no override". */
export function parseResumeOverride(raw: string | null): number | null {
  if (raw == null) return null
  const value = Number(raw)
  return Number.isFinite(value) && value >= 0 ? value : null
}
