import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import { useQueryClient, type InfiniteData, type QueryClient, type QueryKey } from '@tanstack/react-query'
import { useToast } from '../ui/index.ts'
import { ApiError } from './client.ts'
import { showQuery, type LibraryEntryFilters, type LibraryFilters } from './queries.ts'
import type {
  Health,
  ItemList,
  ItemSummary,
  LibraryList,
  RootInfo,
  ShowSummary,
  UploadCompleteEvent,
  UploadProgressEvent,
} from './types.ts'

interface RootStatusPayload {
  id: number
  online: boolean
}

interface RemovedPayload {
  id: number
}

interface LiveItemsValue {
  /** True right after an `item.added` SSE event, to trigger the entrance animation. */
  hasJustArrived: (id: number) => boolean
}

const LiveItemsContext = createContext<LiveItemsValue | null>(null)
// The "New" badge itself is driven by `created_at` (see lib/format.ts
// isRecentlyCreated), so this only needs to outlive the card-enter animation.
const ENTRANCE_ANIMATION_MS = 2_000

export function SSEProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient()
  const { toast } = useToast()
  const [justArrivedIds, setJustArrivedIds] = useState<Set<number>>(() => new Set())

  const markJustArrived = useCallback((id: number) => {
    setJustArrivedIds((current) => new Set(current).add(id))
    window.setTimeout(() => {
      setJustArrivedIds((current) => {
        const next = new Set(current)
        next.delete(id)
        return next
      })
    }, ENTRANCE_ANIMATION_MS)
  }, [])

  useEffect(() => {
    const source = new EventSource('/api/events')

    const refreshItems = () => {
      void queryClient.invalidateQueries({ queryKey: ['items'], refetchType: 'active' })
      void queryClient.invalidateQueries({ queryKey: ['library'], refetchType: 'active' })
      void queryClient.invalidateQueries({ queryKey: ['continue-watching'], refetchType: 'active' })
    }
    const refreshItem = (id: number) => {
      void queryClient.invalidateQueries({ queryKey: ['item', id], refetchType: 'active' })
    }
    const refreshShow = (id: number) => {
      void queryClient.invalidateQueries({ queryKey: ['continue-watching'], refetchType: 'active' })
      void queryClient.invalidateQueries({ queryKey: ['show', id], refetchType: 'active' })
    }
    // A reconnect may have missed events, so heal every list cache — search
    // results live under a separate ['search', q] key (see api/queries.ts).
    const refreshAll = () => {
      refreshItems()
      void queryClient.invalidateQueries({ queryKey: ['search'], refetchType: 'active' })
      void queryClient.invalidateQueries({ queryKey: ['show'], refetchType: 'active' })
    }

    source.onopen = refreshAll
    source.onerror = refreshAll

    source.addEventListener('item.added', (event) => {
      const item = parseEventData<ItemSummary>(event)
      if (!item) return
      markJustArrived(item.id)
      refreshItems()
      refreshItem(item.id)
    })

    // item.updated carries the full summary (SPEC-API), so the paged list
    // caches are patched in place instead of invalidated — an invalidated
    // infinite query refetches every loaded page, which turns each progress
    // save or thumbnail completion into a storm proportional to how far the
    // user has scrolled.
    source.addEventListener('item.updated', (event) => {
      const item = parseEventData<ItemSummary>(event)
      if (!item) return
      patchItemInLists(queryClient, item)
      patchItemInLibrary(queryClient, item)
      refreshItem(item.id)
      if (item.show) {
        void queryClient.invalidateQueries({ queryKey: ['show', item.show.id], refetchType: 'active' })
      }
      // The continue-watching rail is a plain (non-infinite) query with its
      // own membership rules; refetch rather than patch in place.
      void queryClient.invalidateQueries({ queryKey: ['continue-watching'], refetchType: 'active' })
    })

    source.addEventListener('item.removed', (event) => {
      const payload = parseEventData<RemovedPayload>(event)
      if (!payload) return
      queryClient.removeQueries({ queryKey: ['item', payload.id] })
      removeItemFromLists(queryClient, payload.id)
      removeItemFromLibrary(queryClient, payload.id)
      void queryClient.invalidateQueries({ queryKey: ['continue-watching'], refetchType: 'active' })
    })

    // Show events carry only an id. A new show needs its place in the grid,
    // so the grid refetches; otherwise the one card is patched or dropped in
    // place, like items, so a progress save never refetches every page.
    source.addEventListener('show.added', (event) => {
      const payload = parseEventData<RemovedPayload>(event)
      if (!payload) return
      void queryClient.invalidateQueries({ queryKey: ['library'], refetchType: 'active' })
      refreshShow(payload.id)
    })

    source.addEventListener('show.updated', (event) => {
      const payload = parseEventData<RemovedPayload>(event)
      if (!payload) return
      refreshShow(payload.id)
      void patchShowInLibrary(queryClient, payload.id)
    })

    // An open show page refetches into its "not found" state.
    source.addEventListener('show.removed', (event) => {
      const payload = parseEventData<RemovedPayload>(event)
      if (!payload) return
      removeShowFromLibrary(queryClient, payload.id)
      refreshShow(payload.id)
    })

    source.addEventListener('root.status', (event) => {
      const payload = parseEventData<RootStatusPayload>(event)
      if (!payload) return
      const health = queryClient.getQueryData<Health>(['health'])
      const roots = queryClient.getQueryData<RootInfo[]>(['roots'])
      const root =
        roots?.find((candidate) => candidate.id === payload.id) ??
        health?.roots.find((candidate) => candidate.id === payload.id)
      toast({
        message: `${root?.name ?? `Root ${payload.id}`} ${payload.online ? 'connected' : 'disconnected'}`,
      })
      void queryClient.invalidateQueries({ queryKey: ['roots'], refetchType: 'active' })
      void queryClient.invalidateQueries({ queryKey: ['health'], refetchType: 'active' })
      refreshItems()
    })

    source.addEventListener('upload.progress', (event) => {
      const payload = parseEventData<UploadProgressEvent>(event)
      if (!payload) return
      window.dispatchEvent(new CustomEvent('media-server:upload-progress', { detail: payload }))
    })

    source.addEventListener('upload.complete', (event) => {
      const payload = parseEventData<UploadCompleteEvent>(event)
      if (!payload) return
      window.dispatchEvent(new CustomEvent('media-server:upload-complete', { detail: payload }))
    })

    return () => source.close()
  }, [markJustArrived, queryClient, toast])

  const value = useMemo<LiveItemsValue>(
    () => ({
      hasJustArrived: (id: number) => justArrivedIds.has(id),
    }),
    [justArrivedIds],
  )

  return <LiveItemsContext.Provider value={value}>{children}</LiveItemsContext.Provider>
}

// eslint-disable-next-line react-refresh/only-export-components
export function useLiveItems() {
  const ctx = useContext(LiveItemsContext)
  if (!ctx) throw new Error('useLiveItems must be used inside SSEProvider')
  return ctx
}

function parseEventData<T>(event: Event): T | null {
  try {
    return JSON.parse((event as MessageEvent<string>).data) as T
  } catch {
    return null
  }
}

// What a paged list query is scoped to, recovered from its key. Two shapes
// exist (see useLibraryItems): ['items', LibraryFilters] and
// ['search', q, collectionID | null, uncollected].
interface ListScope {
  collectionID: number | undefined
  uncollected: boolean
  trashed: boolean
}

function listScope(key: QueryKey): ListScope | null {
  if (key[0] === 'items') {
    const filters = key[1] as LibraryFilters | undefined
    return {
      collectionID: filters?.collection_id,
      uncollected: filters?.uncollected ?? false,
      trashed: filters?.trashed ?? false,
    }
  }
  if (key[0] === 'search') {
    return {
      collectionID: typeof key[2] === 'number' ? key[2] : undefined,
      uncollected: key[3] === true,
      trashed: false,
    }
  }
  return null
}

function listQueries(queryClient: QueryClient) {
  const cache = queryClient.getQueryCache()
  return [...cache.findAll({ queryKey: ['items'] }), ...cache.findAll({ queryKey: ['search'] })]
}

/** Apply an item.updated summary to every cached library/search page.
    In place when the item is loaded; removal (plus total decrement) when it
    stopped matching a view's filter. An item that isn't loaded is left alone —
    it lives beyond the fetched pages and the next natural refetch covers it —
    except in filtered views it may have just started matching, which only a
    refetch of that view can position correctly. */
function patchItemInLists(queryClient: QueryClient, item: ItemSummary) {
  for (const query of listQueries(queryClient)) {
    const scope = listScope(query.queryKey)
    // Trash membership changes arrive as item.added/removed, not updates.
    if (!scope || scope.trashed) continue
    const data = query.state.data as InfiniteData<ItemList> | undefined
    if (!data) continue
    const matches =
      scope.collectionID != null
        ? item.collection_ids.includes(scope.collectionID)
        : scope.uncollected
          ? item.collection_ids.length === 0
          : true
    const present = data.pages.some((page) => page.items.some((it) => it.id === item.id))
    if (!present) {
      if (matches && (scope.collectionID != null || scope.uncollected)) {
        void queryClient.invalidateQueries({
          queryKey: query.queryKey,
          exact: true,
          refetchType: 'active',
        })
      }
      continue
    }
    queryClient.setQueryData<InfiniteData<ItemList>>(query.queryKey, (old) => {
      if (!old) return old
      return {
        ...old,
        pages: old.pages.map((page) =>
          matches
            ? { ...page, items: page.items.map((it) => (it.id === item.id ? item : it)) }
            : {
                ...page,
                total: Math.max(0, page.total - 1),
                items: page.items.filter((it) => it.id !== item.id),
              },
        ),
      }
    })
  }
}

/** Drop a removed item from every cached library/search page. Trashed views
    refetch instead: a soft delete *adds* the item there. */
function removeItemFromLists(queryClient: QueryClient, id: number) {
  for (const query of listQueries(queryClient)) {
    const scope = listScope(query.queryKey)
    if (!scope) continue
    if (scope.trashed) {
      void queryClient.invalidateQueries({
        queryKey: query.queryKey,
        exact: true,
        refetchType: 'active',
      })
      continue
    }
    const data = query.state.data as InfiniteData<ItemList> | undefined
    if (!data || !data.pages.some((page) => page.items.some((it) => it.id === id))) continue
    queryClient.setQueryData<InfiniteData<ItemList>>(query.queryKey, (old) => {
      if (!old) return old
      return {
        ...old,
        pages: old.pages.map((page) => ({
          ...page,
          total: Math.max(0, page.total - 1),
          items: page.items.filter((it) => it.id !== id),
        })),
      }
    })
  }
}

function libraryQueries(queryClient: QueryClient) {
  return queryClient.getQueryCache().findAll({ queryKey: ['library'] })
}

function hasLibraryItem(data: InfiniteData<LibraryList>, id: number) {
  return data.pages.some((page) => page.entries.some((e) => e.kind === 'item' && e.id === id))
}

/** The library grid's counterpart of patchItemInLists. An item that gained a
    show leaves the grid (its show's own event refetches the show card). */
function patchItemInLibrary(queryClient: QueryClient, item: ItemSummary) {
  for (const query of libraryQueries(queryClient)) {
    const filters = query.queryKey[1] as LibraryEntryFilters | undefined
    const data = query.state.data as InfiniteData<LibraryList> | undefined
    if (!filters || !data) continue
    const matches =
      item.show == null &&
      (filters.type === 'show' ? false : filters.type === 'movie' ? item.type === 'movie' : true) &&
      inCollectionScope(filters, item.collection_ids) &&
      (!filters.unwatched || !(item.progress && (item.progress.completed || item.progress.position_s > 0)))
    if (!hasLibraryItem(data, item.id)) {
      if (matches && narrowed(filters)) {
        void queryClient.invalidateQueries({ queryKey: query.queryKey, exact: true, refetchType: 'active' })
      }
      continue
    }
    queryClient.setQueryData<InfiniteData<LibraryList>>(query.queryKey, (old) => {
      if (!old) return old
      return {
        ...old,
        pages: old.pages.map((page) =>
          matches
            ? {
                ...page,
                entries: page.entries.map((e) =>
                  e.kind === 'item' && e.id === item.id ? { kind: 'item' as const, ...item } : e,
                ),
              }
            : {
                ...page,
                total: Math.max(0, page.total - 1),
                entries: page.entries.filter((e) => !(e.kind === 'item' && e.id === item.id)),
              },
        ),
      }
    })
  }
}

function removeItemFromLibrary(queryClient: QueryClient, id: number) {
  for (const query of libraryQueries(queryClient)) {
    const data = query.state.data as InfiniteData<LibraryList> | undefined
    if (!data || !hasLibraryItem(data, id)) continue
    queryClient.setQueryData<InfiniteData<LibraryList>>(query.queryKey, (old) => {
      if (!old) return old
      return {
        ...old,
        pages: old.pages.map((page) => ({
          ...page,
          total: Math.max(0, page.total - 1),
          entries: page.entries.filter((e) => !(e.kind === 'item' && e.id === id)),
        })),
      }
    })
  }
}

function narrowed(filters: LibraryEntryFilters) {
  return filters.collection_id != null || !!filters.uncollected || !!filters.type || !!filters.unwatched
}

function inCollectionScope(filters: LibraryEntryFilters, collectionIDs: number[]) {
  if (filters.collection_id != null) return collectionIDs.includes(filters.collection_id)
  return filters.uncollected ? collectionIDs.length === 0 : true
}

function hasLibraryShow(data: InfiniteData<LibraryList>, id: number) {
  return data.pages.some((page) => page.entries.some((e) => e.kind === 'show' && e.id === id))
}

/** The show's card as the grid has it, from one fetch of the show; null
    when the show is gone. Undefined when it couldn't be fetched. */
async function fetchShowCard(queryClient: QueryClient, id: number): Promise<ShowSummary | null | undefined> {
  try {
    const detail = await queryClient.fetchQuery(showQuery(id))
    // eslint-disable-next-line @typescript-eslint/no-unused-vars
    const { summary, tvmaze_id, season_offset, title_locked, seasons, ...card } = detail
    return card.episode_count > 0 ? card : null
  } catch (error) {
    return error instanceof ApiError && error.status === 404 ? null : undefined
  }
}

/** The show-card counterpart of patchItemInLibrary, fetching the show at
    most once and only when a loaded view holds its card or may gain it. */
async function patchShowInLibrary(queryClient: QueryClient, id: number) {
  const affected = libraryQueries(queryClient).filter((query) => {
    const filters = query.queryKey[1] as LibraryEntryFilters | undefined
    const data = query.state.data as InfiniteData<LibraryList> | undefined
    return filters && data && (hasLibraryShow(data, id) || narrowed(filters))
  })
  if (affected.length === 0) return
  const show = await fetchShowCard(queryClient, id)
  if (show === undefined) return
  if (show === null) {
    removeShowFromLibrary(queryClient, id)
    return
  }
  for (const query of affected) {
    const filters = query.queryKey[1] as LibraryEntryFilters
    const data = query.state.data as InfiniteData<LibraryList> | undefined
    if (!data) continue
    const matches =
      filters.type !== 'movie' && inCollectionScope(filters, show.collection_ids) && !(filters.unwatched && show.has_progress)
    if (!hasLibraryShow(data, id)) {
      if (matches && narrowed(filters)) {
        void queryClient.invalidateQueries({ queryKey: query.queryKey, exact: true, refetchType: 'active' })
      }
      continue
    }
    queryClient.setQueryData<InfiniteData<LibraryList>>(query.queryKey, (old) => {
      if (!old) return old
      return {
        ...old,
        pages: old.pages.map((page) =>
          matches
            ? {
                ...page,
                entries: page.entries.map((e) => (e.kind === 'show' && e.id === id ? { kind: 'show' as const, ...show } : e)),
              }
            : {
                ...page,
                total: Math.max(0, page.total - 1),
                entries: page.entries.filter((e) => !(e.kind === 'show' && e.id === id)),
              },
        ),
      }
    })
  }
}

function removeShowFromLibrary(queryClient: QueryClient, id: number) {
  for (const query of libraryQueries(queryClient)) {
    const data = query.state.data as InfiniteData<LibraryList> | undefined
    if (!data || !hasLibraryShow(data, id)) continue
    queryClient.setQueryData<InfiniteData<LibraryList>>(query.queryKey, (old) => {
      if (!old) return old
      return {
        ...old,
        pages: old.pages.map((page) => ({
          ...page,
          total: Math.max(0, page.total - 1),
          entries: page.entries.filter((e) => !(e.kind === 'show' && e.id === id)),
        })),
      }
    })
  }
}
