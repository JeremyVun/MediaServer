import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, jsonApi } from './client.ts'
import type {
  Collection,
  Health,
  RootInfo,
  FsDirs,
  ItemDetail,
  ItemList,
  ItemPatch,
  Job,
  AddRootRequest,
  PlayRequest,
  PlayResponse,
  Progress,
  ProgressUpdate,
  PurgeTrashResponse,
  RescanResponse,
} from './types.ts'

// Sized to what the home screen can actually show before the sentinel pulls
// more: ~3 visible rows + 2 buffer rows of 4–5 columns.
const PAGE_SIZE = 30

export interface LibraryFilters {
  q: string
  sort?: 'added' | 'title' | 'year'
  order?: 'asc' | 'desc'
  type?: 'video' | 'movie' | 'episode'
  collection_id?: number
  uncollected?: boolean
  trashed?: boolean
}

export function useHealth() {
  return useQuery({
    queryKey: ['health'],
    queryFn: () => api<Health>('/api/health'),
    refetchInterval: 30_000,
  })
}

export function useRoots() {
  return useQuery({
    queryKey: ['roots'],
    queryFn: () => api<RootInfo[]>('/api/roots'),
    refetchInterval: 30_000,
  })
}

export function useFsDirs(path: string, enabled = true) {
  const params = new URLSearchParams()
  params.set('path', path)
  return useQuery({
    queryKey: ['fs-dirs', path],
    queryFn: () => api<FsDirs>(`/api/fs/dirs?${params}`),
    enabled,
  })
}

export function useAddRoot() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (root: AddRootRequest) => jsonApi<RootInfo>('/api/roots', 'POST', root),
    onSuccess: () => invalidateRootCaches(queryClient),
  })
}

export function useDetachRoot() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api<void>(`/api/roots/${id}`, { method: 'DELETE' }),
    onSuccess: () => invalidateRootCaches(queryClient),
  })
}

export function useRescanRoot() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api<RescanResponse>(`/api/roots/${id}/rescan`, { method: 'POST' }),
    onSuccess: () => invalidateRootCaches(queryClient),
  })
}

export function useCollections() {
  return useQuery({
    queryKey: ['collections'],
    queryFn: () => api<Collection[]>('/api/collections'),
  })
}

export function useCreateCollection() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (name: string) => jsonApi<Collection>('/api/collections', 'POST', { name }),
    onSuccess: () => invalidateCollectionCaches(queryClient),
  })
}

export function useRenameCollection() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ id, name }: { id: number; name: string }) =>
      jsonApi<Collection>(`/api/collections/${id}`, 'PATCH', { name }),
    onSuccess: () => invalidateCollectionCaches(queryClient),
  })
}

export function useDeleteCollection() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api<void>(`/api/collections/${id}`, { method: 'DELETE' }),
    onSuccess: () => invalidateCollectionCaches(queryClient),
  })
}

export function useAddItemToCollection() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ collectionID, itemID }: { collectionID: number; itemID: number }) =>
      jsonApi<void>(`/api/collections/${collectionID}/items`, 'POST', { item_id: itemID }),
    onSuccess: () => invalidateCollectionCaches(queryClient),
  })
}

export function useRemoveItemFromCollection() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ collectionID, itemID }: { collectionID: number; itemID: number }) =>
      api<void>(`/api/collections/${collectionID}/items/${itemID}`, { method: 'DELETE' }),
    onSuccess: () => invalidateCollectionCaches(queryClient),
  })
}

export function useReorderCollection() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ collectionID, itemIDs }: { collectionID: number; itemIDs: number[] }) =>
      jsonApi<void>(`/api/collections/${collectionID}/order`, 'PUT', { item_ids: itemIDs }),
    onSuccess: () => invalidateCollectionCaches(queryClient),
  })
}

export function useLibraryItems(filters: LibraryFilters) {
  const normalizedQ = filters.q.trim()
  return useInfiniteQuery({
    queryKey: normalizedQ
      ? ['search', normalizedQ, filters.collection_id ?? null, filters.uncollected ?? false]
      : ['items', filters],
    initialPageParam: 0,
    queryFn: ({ pageParam }) => {
      const params = new URLSearchParams()
      if (normalizedQ) {
        params.set('q', normalizedQ)
        params.set('limit', String(PAGE_SIZE))
        if (filters.collection_id) params.set('collection_id', String(filters.collection_id))
        else if (filters.uncollected) params.set('uncollected', '1')
        return api<ItemList>(`/api/search?${params}`)
      }
      if (filters.sort) params.set('sort', filters.sort)
      if (filters.order) params.set('order', filters.order)
      if (filters.type) params.set('type', filters.type)
      if (filters.collection_id) params.set('collection_id', String(filters.collection_id))
      else if (filters.uncollected) params.set('uncollected', '1')
      if (filters.trashed) params.set('trashed', '1')
      params.set('offset', String(pageParam))
      params.set('limit', String(PAGE_SIZE))
      return api<ItemList>(`/api/items?${params}`)
    },
    getNextPageParam: (lastPage, pages) => {
      if (normalizedQ) return undefined
      const loaded = pages.reduce((sum, page) => sum + page.items.length, 0)
      return loaded < lastPage.total ? loaded : undefined
    },
  })
}

// "Continue watching" rail: server-side filter so it sees the whole catalog,
// not just the library pages fetched so far. Over-fetches past the display cap
// so the client can drop offline items and still fill the row.
export function useContinueWatching(enabled: boolean) {
  return useQuery({
    queryKey: ['continue-watching'],
    queryFn: () => api<ItemList>('/api/items?in_progress=1&sort=watched&limit=24'),
    enabled,
  })
}

export function useItem(id: number | null) {
  return useQuery({
    queryKey: ['item', id],
    queryFn: () => api<ItemDetail>(`/api/items/${id}`),
    enabled: id != null,
  })
}

export function usePatchItem(id: number) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (patch: ItemPatch) => jsonApi<ItemDetail>(`/api/items/${id}`, 'PATCH', patch),
    onSuccess: (item) => {
      queryClient.setQueryData(['item', id], item)
      void queryClient.invalidateQueries({ queryKey: ['items'] })
    },
  })
}

export function useDeleteItem() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api<void>(`/api/items/${id}`, { method: 'DELETE' }),
    onSuccess: () => invalidateItemCaches(queryClient),
  })
}

export function useRestoreItem() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api<{ id: number }>(`/api/items/${id}/restore`, { method: 'POST' }),
    onSuccess: () => invalidateItemCaches(queryClient),
  })
}

export function usePurgeItem() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api<void>(`/api/items/${id}/purge`, { method: 'DELETE' }),
    onSuccess: () => invalidateItemCaches(queryClient),
  })
}

export function usePurgeTrash() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => api<PurgeTrashResponse>('/api/trash/purge', { method: 'POST' }),
    onSuccess: () => invalidateItemCaches(queryClient),
  })
}

export function useJobs(status?: Job['status']) {
  const params = new URLSearchParams()
  if (status) params.set('status', status)
  return useQuery({
    queryKey: ['jobs', status ?? 'all'],
    queryFn: () => api<Job[]>(`/api/jobs${params.size ? `?${params}` : ''}`),
    refetchInterval: 30_000,
  })
}

export function useRetryJob() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api<Job>(`/api/jobs/${id}/retry`, { method: 'POST' }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['jobs'] })
      void queryClient.invalidateQueries({ queryKey: ['health'] })
    },
  })
}

export function usePlayItem(id: number, request: PlayRequest | null) {
  return useQuery({
    queryKey: ['play', id, request],
    queryFn: () => jsonApi<PlayResponse>(`/api/items/${id}/play`, 'POST', request),
    enabled: request != null,
    refetchOnWindowFocus: false,
    retry: false,
    // A play response is disposable server state, not a cacheable resource: the
    // player opens an HLS session on mount and DELETEs it on unmount. Bind the
    // cached response to the player's lifetime — gcTime: 0 drops it the instant
    // the player unmounts, staleTime: 0 never re-serves it — so re-entering the
    // player mints a fresh session instead of pointing <video> at the session
    // the previous visit already tore down (a 404 → "Playback failed").
    staleTime: 0,
    gcTime: 0,
  })
}

export function useSaveProgress(id: number) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (progress: ProgressUpdate) =>
      jsonApi<Progress>(`/api/items/${id}/progress`, 'PUT', progress),
    onSuccess: (progress) => {
      queryClient.setQueryData<ItemDetail>(['item', id], (item) =>
        item ? { ...item, progress } : item,
      )
      void queryClient.invalidateQueries({ queryKey: ['items'], refetchType: 'active' })
    },
  })
}

export function saveProgressBeacon(id: number, progress: ProgressUpdate) {
  void fetch(`/api/items/${id}/progress`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(progress),
    keepalive: true,
  })
}

export function deletePlaybackSession(sessionID: string) {
  void fetch(`/api/sessions/${sessionID}`, {
    method: 'DELETE',
    keepalive: true,
  })
}

export function beaconPlaybackSession(sessionID: string) {
  if (navigator.sendBeacon) {
    navigator.sendBeacon(`/api/sessions/${sessionID}/teardown`, new Blob([], { type: 'text/plain' }))
    return
  }
  deletePlaybackSession(sessionID)
}

function invalidateRootCaches(queryClient: ReturnType<typeof useQueryClient>) {
  void queryClient.invalidateQueries({ queryKey: ['roots'] })
  void queryClient.invalidateQueries({ queryKey: ['health'] })
  invalidateItemCaches(queryClient)
}

function invalidateCollectionCaches(queryClient: ReturnType<typeof useQueryClient>) {
  void queryClient.invalidateQueries({ queryKey: ['collections'] })
  invalidateItemCaches(queryClient)
}

function invalidateItemCaches(queryClient: ReturnType<typeof useQueryClient>) {
  void queryClient.invalidateQueries({ queryKey: ['items'] })
  void queryClient.invalidateQueries({ queryKey: ['item'] })
  void queryClient.invalidateQueries({ queryKey: ['search'] })
  void queryClient.invalidateQueries({ queryKey: ['collections'] })
}
