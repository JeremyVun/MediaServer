import { useEffect, useRef, useState, type DragEvent, type PointerEvent } from 'react'
import { Link, useLocation, useNavigate, useSearchParams } from 'react-router'
import {
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  Clapperboard,
  HardDrive,
  Info,
  Layers,
  MoreHorizontal,
  Play,
  Plus,
  Search,
  Settings,
  Trash2,
  Upload,
  X,
} from 'lucide-react'
import {
  useAddItemToCollection,
  useCollections,
  useContinueWatching,
  useCreateCollection,
  useDeleteItem,
  useHealth,
  useLibraryItems,
  useRemoveItemFromCollection,
  useRestoreItem,
  type LibraryFilters,
} from '../../api/queries.ts'
import { useLiveItems } from '../../api/sse.tsx'
import { useUploads } from '../upload/UploadProvider.tsx'
import type { Collection, ItemSummary } from '../../api/types.ts'
import { formatDuration, isRecentlyCreated, progressPercent } from '../../lib/format.ts'
import { makeRunOrToast } from '../../lib/mutationFeedback.ts'
import { libraryParamUpdates, parseLibraryParams, withParamUpdates } from '../../lib/searchParams.ts'
import {
  Button,
  Card,
  Dialog,
  IconButton,
  Input,
  Menu,
  MenuItem,
  MenuSeparator,
  Skeleton,
  useToast,
} from '../../ui/index.ts'

const SORT_OPTIONS: { value: NonNullable<LibraryFilters['sort']>; label: string }[] = [
  { value: 'added', label: 'Recently added' },
  { value: 'title', label: 'Title' },
  { value: 'year', label: 'Year' },
]

export function LibraryPage() {
  const health = useHealth()
  const location = useLocation()
  const [searchParams, setSearchParams] = useSearchParams()
  const urlState = parseLibraryParams(searchParams)
  const sort = urlState.sort
  const collectionID = urlState.collectionID
  const uncollected = urlState.uncollected

  // 3.3: q/sort/collection live in the URL so the library survives a
  // round-trip and is shareable. `query` is local so typing feels instant;
  // committing it to the URL is debounced via a plain timer (replace: true —
  // no history spam), so `urlState.q` itself is the debounced value used for
  // the actual query. When the URL's q changes from elsewhere (e.g. browser
  // back), re-seed local state during render — the React-endorsed
  // alternative to an effect-based sync (avoids setState-in-effect, which
  // triggers a cascading render and is flagged by the hooks lint rule).
  const [query, setQuery] = useState(urlState.q)
  const [syncedUrlQuery, setSyncedUrlQuery] = useState(urlState.q)
  if (urlState.q !== syncedUrlQuery) {
    setSyncedUrlQuery(urlState.q)
    setQuery(urlState.q)
  }

  const queryCommitTimer = useRef<number | null>(null)
  useEffect(() => () => {
    if (queryCommitTimer.current != null) window.clearTimeout(queryCommitTimer.current)
  }, [])

  const onQueryChange = (value: string) => {
    setQuery(value)
    if (queryCommitTimer.current != null) window.clearTimeout(queryCommitTimer.current)
    queryCommitTimer.current = window.setTimeout(() => {
      queryCommitTimer.current = null
      setSyncedUrlQuery(value)
      setSearchParams((prev) => withParamUpdates(prev, { q: value ? value : undefined }), { replace: true })
    }, 150)
  }

  const setSort = (next: LibraryFilters['sort']) =>
    setSearchParams(
      (prev) =>
        withParamUpdates(
          prev,
          libraryParamUpdates({ q: urlState.q, sort: next ?? 'added', collectionID, uncollected }),
        ),
      { replace: true },
    )
  // The collection filter is one of three mutually exclusive pills: undefined
  // (All), 'none' (Unsorted), or a specific collection id.
  const setCollectionFilter = (next: number | 'none' | undefined) =>
    setSearchParams(
      (prev) =>
        withParamUpdates(
          prev,
          libraryParamUpdates({
            q: urlState.q,
            sort,
            collectionID: typeof next === 'number' ? next : undefined,
            uncollected: next === 'none',
          }),
        ),
      { replace: true },
    )
  const pillClass = (active: boolean) =>
    [
      'border-line rounded-full border px-3 py-1.5 text-sm',
      active ? 'bg-accent-fill text-accent-contrast' : 'bg-surface text-primary hover:bg-raised',
    ].join(' ')

  // The item/watch pages send the user back here via router state — built
  // from the live `query` (not the debounced `urlState.q`) so a poster click
  // right after typing still carries the not-yet-committed search text.
  const libraryPath = (() => {
    const qs = withParamUpdates(
      searchParams,
      libraryParamUpdates({ q: query, sort, collectionID, uncollected }),
    ).toString()
    return qs ? `/?${qs}` : '/'
  })()

  // React Query serializes the query key by value, so this doesn't need to
  // be a stable reference across renders.
  const filters: LibraryFilters = { q: urlState.q, sort, collection_id: collectionID, uncollected }
  const library = useLibraryItems(filters)
  const collections = useCollections()
  const addToCollection = useAddItemToCollection()
  const removeFromCollection = useRemoveItemFromCollection()
  const createCollection = useCreateCollection()
  const deleteItem = useDeleteItem()
  const restoreItem = useRestoreItem()
  const liveItems = useLiveItems()
  const uploads = useUploads()
  const { toast } = useToast()
  const items = library.data?.pages.flatMap((page) => page.items) ?? []
  const total = library.data?.pages[0]?.total ?? 0
  // Phase 5: "Continue watching" — dedicated server-side query (in_progress
  // filter, most recently watched first) so it covers the whole catalog, not
  // just the library pages fetched so far. Only on the default view: any
  // active search or collection filter narrows the intent, so we hide it
  // there. Capped: it's a shortcut row, not a second library.
  const showContinueWatching = !urlState.q && collectionID == null && !uncollected
  const continueQuery = useContinueWatching(showContinueWatching)
  const continueWatching = showContinueWatching
    ? (continueQuery.data?.items ?? []).filter((item) => item.available).slice(0, 8)
    : []
  const sentinelRef = useRef<HTMLDivElement | null>(null)
  const fileInputRef = useRef<HTMLInputElement | null>(null)
  const dragDepth = useRef(0)
  const [draggingFiles, setDraggingFiles] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<ItemSummary | null>(null)

  // 4.1: selection mode. null = off; a Set (possibly empty) = active.
  // Per-visit state by design — never in the URL; exiting clears it.
  const [selection, setSelection] = useState<Set<number> | null>(null)
  const selectionAnchor = useRef<number | null>(null)
  const [batchDeleteOpen, setBatchDeleteOpen] = useState(false)
  const [batchFilter, setBatchFilter] = useState('')
  const selectionActive = selection != null

  const enterSelection = (id: number) => {
    selectionAnchor.current = id
    setSelection(new Set([id]))
  }
  const exitSelection = () => {
    selectionAnchor.current = null
    setSelection(null)
    setBatchDeleteOpen(false)
  }
  const toggleSelected = (id: number, shiftRange: boolean) => {
    setSelection((prev) => {
      if (!prev) return prev
      const next = new Set(prev)
      const anchor = selectionAnchor.current
      if (shiftRange && anchor != null) {
        const ids = items.map((item) => item.id)
        const from = ids.indexOf(anchor)
        const to = ids.indexOf(id)
        if (from !== -1 && to !== -1) {
          for (let i = Math.min(from, to); i <= Math.max(from, to); i++) next.add(ids[i])
          return next
        }
      }
      if (next.has(id)) next.delete(id)
      else next.add(id)
      selectionAnchor.current = id
      return next
    })
  }

  useEffect(() => {
    if (!selectionActive) return
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      // A dialog, open menu, or focused field owns Escape.
      const target = event.target instanceof Element ? event.target : null
      if (target?.closest('dialog, [popover], input, textarea')) return
      exitSelection()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [selectionActive])

  useEffect(() => {
    const node = sentinelRef.current
    const hasNextPage = library.hasNextPage
    const isFetchingNextPage = library.isFetchingNextPage
    const fetchNextPage = library.fetchNextPage
    if (!node || !hasNextPage) return
    const observer = new IntersectionObserver((entries) => {
      if (entries[0]?.isIntersecting && !isFetchingNextPage) {
        void fetchNextPage()
      }
    })
    observer.observe(node)
    return () => observer.disconnect()
  }, [library.hasNextPage, library.isFetchingNextPage, library.fetchNextPage])

  // 3.3: remember scroll position per history entry (location.key) so
  // returning from an item via browser back lands roughly where the user
  // left off, instead of resetting to the top.
  const scrollKey = `library-scroll:${location.key}`
  const scrollRestored = useRef(false)
  useEffect(() => {
    // A fresh navigation (new key) gets a fresh restore attempt.
    scrollRestored.current = false
  }, [scrollKey])

  useEffect(() => {
    let frame = 0
    const save = () => {
      if (frame) return
      frame = window.requestAnimationFrame(() => {
        frame = 0
        sessionStorage.setItem(scrollKey, String(window.scrollY))
      })
    }
    window.addEventListener('scroll', save, { passive: true })
    return () => {
      window.removeEventListener('scroll', save)
      if (frame) window.cancelAnimationFrame(frame)
      // Save the final position before navigating away, in case the last
      // scroll event was throttled out.
      sessionStorage.setItem(scrollKey, String(window.scrollY))
    }
  }, [scrollKey])

  useEffect(() => {
    if (scrollRestored.current || library.isPending) return
    const saved = Number(sessionStorage.getItem(scrollKey))
    if (!Number.isFinite(saved) || saved <= 0) {
      scrollRestored.current = true
      return
    }
    window.scrollTo(0, saved)
    // The saved position may be deeper than what the first page renders —
    // keep loading pages until we can reach it (or run out of pages).
    if (window.scrollY < saved - 2 && library.hasNextPage && !library.isFetchingNextPage) {
      void library.fetchNextPage()
      return
    }
    scrollRestored.current = true
    // `library` is a fresh object every render (useInfiniteQuery); depending
    // on specific fields below avoids re-running this effect on every
    // unrelated re-render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    scrollKey,
    library.isPending,
    library.isFetchingNextPage,
    library.hasNextPage,
    library.fetchNextPage,
    items.length,
  ])

  const onFileInput = () => {
    const files = fileInputRef.current?.files
    if (files) uploads.addFiles(files)
    if (fileInputRef.current) fileInputRef.current.value = ''
  }

  const onDragEnter = (event: DragEvent<HTMLElement>) => {
    if (!hasFiles(event)) return
    event.preventDefault()
    dragDepth.current += 1
    setDraggingFiles(true)
  }

  const onDragOver = (event: DragEvent<HTMLElement>) => {
    if (!hasFiles(event)) return
    event.preventDefault()
    event.dataTransfer.dropEffect = 'copy'
  }

  const onDragLeave = (event: DragEvent<HTMLElement>) => {
    if (!hasFiles(event)) return
    dragDepth.current = Math.max(0, dragDepth.current - 1)
    if (dragDepth.current === 0) setDraggingFiles(false)
  }

  const onDrop = (event: DragEvent<HTMLElement>) => {
    if (!hasFiles(event)) return
    event.preventDefault()
    dragDepth.current = 0
    setDraggingFiles(false)
    uploads.addFiles(event.dataTransfer.files)
  }

  const runOrToast = makeRunOrToast(toast)

  const onToggleCollection = (item: ItemSummary, collection: Collection, isMember: boolean) =>
    runOrToast(
      () =>
        isMember
          ? removeFromCollection.mutateAsync({ collectionID: collection.id, itemID: item.id })
          : addToCollection.mutateAsync({ collectionID: collection.id, itemID: item.id }),
      "Couldn't update collection",
    )

  const onCreateCollectionAndAdd = (item: ItemSummary, name: string) =>
    runOrToast(async () => {
      const collection = await createCollection.mutateAsync(name)
      await addToCollection.mutateAsync({ collectionID: collection.id, itemID: item.id })
    }, "Couldn't create collection")

  const onDelete = async () => {
    if (!deleteTarget) return
    const item = deleteTarget
    const ok = await runOrToast(() => deleteItem.mutateAsync(item.id), "Couldn't move to trash")
    if (!ok) return
    setDeleteTarget(null)
    toast({
      message: `${item.title} moved to trash`,
      action: {
        label: 'Undo',
        onClick: () => {
          void runOrToast(() => restoreItem.mutateAsync(item.id), "Couldn't restore item")
        },
      },
    })
  }

  // Batch actions act on the intersection of the selection and the currently
  // loaded items — ids that vanished (or arrived via SSE untracked) are
  // ignored rather than erroring.
  const selectedItems = () => items.filter((item) => selection?.has(item.id))

  const onBatchAddToCollection = async (collection: Collection) => {
    const targets = selectedItems()
    const ok = await runOrToast(async () => {
      for (const item of targets) {
        if (item.collection_ids.includes(collection.id)) continue
        await addToCollection.mutateAsync({ collectionID: collection.id, itemID: item.id })
      }
    }, "Couldn't add to collection")
    if (!ok) return
    toast({ message: `Added ${countLabel(targets.length)} to ${collection.name}` })
    exitSelection()
  }

  const onBatchCreateCollectionAndAdd = async (name: string) => {
    const ok = await runOrToast(async () => {
      const collection = await createCollection.mutateAsync(name)
      for (const item of selectedItems()) {
        await addToCollection.mutateAsync({ collectionID: collection.id, itemID: item.id })
      }
    }, "Couldn't create collection")
    if (!ok) return
    toast({ message: `Added ${countLabel(selectedItems().length)} to ${name}` })
    exitSelection()
  }

  const onBatchDelete = async () => {
    const targets = selectedItems()
    const trashed: ItemSummary[] = []
    const ok = await runOrToast(async () => {
      for (const item of targets) {
        await deleteItem.mutateAsync(item.id)
        trashed.push(item)
      }
    }, "Couldn't move to trash")
    setBatchDeleteOpen(false)
    if (trashed.length > 0) {
      toast({
        message: `${countLabel(trashed.length)} moved to trash`,
        action: {
          label: 'Undo',
          onClick: () => {
            void runOrToast(async () => {
              for (const item of trashed) await restoreItem.mutateAsync(item.id)
            }, "Couldn't restore items")
          },
        },
      })
    }
    if (ok) exitSelection()
  }

  return (
    <main
      className={[
        'mx-auto max-w-7xl px-4 py-6 sm:px-6 lg:px-8',
        selectionActive ? 'pb-28' : '',
      ].join(' ')}
      onDragEnter={onDragEnter}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
    >
      {draggingFiles && (
        <div className="bg-overlay fixed inset-0 z-[var(--z-dialog)] flex items-center justify-center backdrop-blur-md">
          <div className="border-accent bg-raised text-primary flex items-center gap-3 rounded-md border px-5 py-4 shadow-overlay">
            <Upload aria-hidden className="text-accent size-6" strokeWidth={1.75} />
            <span className="text-lg font-semibold">Upload</span>
          </div>
        </div>
      )}
      <header className="mb-6 flex flex-col gap-4 lg:flex-row lg:items-end lg:justify-between">
        <div>
          <h1 className="text-xl font-semibold">Library</h1>
          <p className="text-sm text-secondary">
            {health.data
              ? `${health.data.roots.filter((r) => r.online).length}/${health.data.roots.length} roots online`
              : 'Checking roots'}
          </p>
        </div>
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
          {/* Actions cluster: Upload fills the row on mobile, the two utility
              icons tuck in beside it instead of each claiming a full row. */}
          <div className="flex items-center gap-3">
            <Button
              variant="primary"
              touch
              className="flex-1 sm:flex-none"
              onClick={() => fileInputRef.current?.click()}
            >
              <Upload aria-hidden className="size-5" strokeWidth={1.75} />
              Upload
            </Button>
            <Link
              to="/collections"
              aria-label="Collections"
              className="bg-surface border-line text-primary hover:bg-raised inline-flex size-11 shrink-0 items-center justify-center rounded-md border"
            >
              <Layers aria-hidden className="size-5" strokeWidth={1.75} />
            </Link>
            <input
              ref={fileInputRef}
              type="file"
              multiple
              aria-label="Upload video files"
              className="sr-only"
              accept="video/*,.mkv,.m4v,.mov,.webm,.avi,.ts,.m2ts,.wmv,.flv"
              onChange={onFileInput}
            />
            <Link
              to="/settings"
              aria-label="Settings"
              className="bg-surface border-line text-primary hover:bg-raised inline-flex size-11 shrink-0 items-center justify-center rounded-md border"
            >
              <Settings aria-hidden className="size-5" strokeWidth={1.75} />
            </Link>
          </div>
          {/* Filter cluster: search grows to fill, sort sits alongside it. */}
          <div className="flex items-center gap-3">
            <Input
              value={query}
              onChange={(e) => onQueryChange(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Escape') onQueryChange('')
              }}
              icon={<Search className="size-4" strokeWidth={1.75} />}
              placeholder="Search"
              aria-label="Search library"
              className="min-w-0 flex-1 sm:w-80 sm:flex-none"
            />
            {query && (
              <IconButton aria-label="Clear search" onClick={() => onQueryChange('')}>
                <X aria-hidden className="size-5" strokeWidth={1.75} />
              </IconButton>
            )}
            <Menu
              aria-label="Sort"
              trigger={
                <>
                  {SORT_OPTIONS.find((option) => option.value === sort)?.label}
                  <ChevronDown aria-hidden className="size-4" strokeWidth={1.75} />
                </>
              }
              triggerClassName="bg-inset border-line-strong text-primary inline-flex h-10 shrink-0 cursor-pointer items-center gap-2 rounded-sm border px-3 text-base"
            >
              {SORT_OPTIONS.map((option) => (
                <MenuItem
                  key={option.value}
                  checked={sort === option.value}
                  onSelect={() => setSort(option.value)}
                >
                  {option.label}
                </MenuItem>
              ))}
            </Menu>
          </div>
        </div>
      </header>

      {collections.data && collections.data.length > 0 && (
        <div className="mb-5 flex flex-wrap gap-2">
          <button
            type="button"
            onClick={() => setCollectionFilter(undefined)}
            className={pillClass(collectionID == null && !uncollected)}
          >
            All
          </button>
          <button
            type="button"
            onClick={() => setCollectionFilter('none')}
            className={pillClass(uncollected)}
          >
            Unsorted
          </button>
          {collections.data.map((collection) => (
            <button
              key={collection.id}
              type="button"
              onClick={() => setCollectionFilter(collection.id)}
              className={pillClass(collectionID === collection.id)}
            >
              {collection.name}
            </button>
          ))}
        </div>
      )}

      {library.isPending && (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(160px,1fr))] gap-4 sm:grid-cols-[repeat(auto-fill,minmax(240px,1fr))]">
          {Array.from({ length: 12 }, (_, i) => (
            <Skeleton key={i} className="aspect-video" />
          ))}
        </div>
      )}

      {library.isError && <EmptyMessage text="Can't load the library" />}

      {continueWatching.length > 0 && <ContinueWatchingRow items={continueWatching} />}

      {!library.isPending && !library.isError && items.length === 0 && (
        <EmptyMessage
          text={
            urlState.q
              ? 'No matching videos'
              : uncollected
                ? 'Everything is in a collection'
                : 'Library is empty'
          }
        />
      )}

      {items.length > 0 && (
        <>
          <div className="mb-4 flex items-center justify-between text-sm text-secondary">
            <span>{total} items</span>
            {urlState.q && <span>Search results for "{urlState.q}"</span>}
          </div>
          <div className="grid grid-cols-[repeat(auto-fill,minmax(160px,1fr))] gap-4 sm:grid-cols-[repeat(auto-fill,minmax(240px,1fr))]">
            {items.map((item) => (
              <PosterCard
                key={item.id}
                item={item}
                isNew={isRecentlyCreated(item.created_at)}
                justArrived={liveItems.hasJustArrived(item.id)}
                libraryPath={libraryPath}
                collections={collections.data ?? []}
                onToggleCollection={onToggleCollection}
                onCreateCollectionAndAdd={onCreateCollectionAndAdd}
                onRequestDelete={setDeleteTarget}
                selectionMode={selectionActive}
                selected={selection?.has(item.id) ?? false}
                onEnterSelection={enterSelection}
                onToggleSelect={toggleSelected}
              />
            ))}
          </div>
          <div ref={sentinelRef} className="h-12" aria-hidden />
          {library.isFetchingNextPage && (
            <div className="grid grid-cols-[repeat(auto-fill,minmax(160px,1fr))] gap-4 sm:grid-cols-[repeat(auto-fill,minmax(240px,1fr))]">
              {Array.from({ length: 6 }, (_, i) => (
                <Skeleton key={i} className="aspect-video" />
              ))}
            </div>
          )}
        </>
      )}

      <Dialog
        open={deleteTarget != null}
        onClose={() => setDeleteTarget(null)}
        title="Move to trash?"
        footer={
          <>
            <Button variant="ghost" onClick={() => setDeleteTarget(null)}>
              Cancel
            </Button>
            <Button variant="danger" pending={deleteItem.isPending} onClick={() => void onDelete()}>
              <Trash2 aria-hidden className="size-4" strokeWidth={1.75} />
              Move to trash
            </Button>
          </>
        }
      >
        <p className="text-secondary">{deleteTarget?.title} will move to the root's trash folder.</p>
      </Dialog>

      {/* 4.1: selection action bar — below toasts (--z-toast) so feedback
          stays visible above it. */}
      {selection && (
        <div className="fixed inset-x-0 bottom-4 z-[var(--z-tray)] flex justify-center px-4">
          <div className="bg-raised border-line shadow-overlay flex flex-wrap items-center gap-2 rounded-lg border px-4 py-2">
            <span className="text-sm text-secondary tabular mr-2" role="status">
              {selection.size} selected
            </span>
            <Menu
              aria-label="Add selected items to collection"
              trigger={
                <>
                  <Plus aria-hidden className="size-4" strokeWidth={1.75} />
                  Add to collection…
                </>
              }
              triggerClassName="bg-surface border-line-strong text-primary hover:bg-raised inline-flex h-9 cursor-pointer items-center gap-2 rounded-md border px-4 text-base font-medium disabled:cursor-not-allowed disabled:opacity-50"
              onOpenChange={(open) => {
                if (!open) setBatchFilter('')
              }}
            >
              <CollectionListPicker
                collections={collections.data ?? []}
                filter={batchFilter}
                setFilter={setBatchFilter}
                onPick={(collection) => void onBatchAddToCollection(collection)}
                onCreate={(name) => void onBatchCreateCollectionAndAdd(name)}
              />
            </Menu>
            <Button
              variant="danger"
              disabled={selection.size === 0}
              onClick={() => setBatchDeleteOpen(true)}
            >
              <Trash2 aria-hidden className="size-4" strokeWidth={1.75} />
              Move to trash
            </Button>
            <Button variant="ghost" onClick={exitSelection}>
              Cancel
            </Button>
          </div>
        </div>
      )}

      <Dialog
        open={batchDeleteOpen}
        onClose={() => setBatchDeleteOpen(false)}
        title="Move to trash?"
        footer={
          <>
            <Button variant="ghost" onClick={() => setBatchDeleteOpen(false)}>
              Cancel
            </Button>
            <Button variant="danger" pending={deleteItem.isPending} onClick={() => void onBatchDelete()}>
              <Trash2 aria-hidden className="size-4" strokeWidth={1.75} />
              Move to trash
            </Button>
          </>
        }
      >
        <p className="text-secondary">
          {countLabel(selection?.size ?? 0)} will move to the root's trash folder.
        </p>
      </Dialog>
    </main>
  )
}

function countLabel(count: number): string {
  return count === 1 ? '1 item' : `${count} items`
}

// Phase 5: horizontal "Continue watching" strip. Its own <section> container so
// its cards' keys never collide with the grid's PosterCards below (same item
// ids appear in both). Compact cards link straight to /watch to resume.
function ContinueWatchingRow({ items }: { items: ItemSummary[] }) {
  const trackRef = useRef<HTMLDivElement>(null)
  // Mouse drag-to-swipe. Touch devices get native swipe + CSS snap from
  // overflow scroll; this adds the same gesture for desktop pointers. CSS
  // snap is suspended while dragging (it fights direct scrollLeft writes).
  // Release keeps the flick's momentum: velocity is sampled during the drag,
  // the destination is projected from it, snapped to a card edge, and the row
  // decelerates there with an rAF ease-out — so a fast flick launches at the
  // hand's speed and eases in, instead of the browser's canned smooth-scroll.
  const drag = useRef({
    active: false,
    moved: false,
    startX: 0,
    startScroll: 0,
    lastX: 0,
    lastT: 0,
    velocity: 0, // px/ms, positive = scrolling right
  })
  const glide = useRef<number | null>(null)

  const stopGlide = () => {
    if (glide.current != null) {
      cancelAnimationFrame(glide.current)
      glide.current = null
    }
  }
  useEffect(() => stopGlide, [])

  const onPointerDown = (e: PointerEvent) => {
    if (e.pointerType !== 'mouse' || e.button !== 0) return
    const el = trackRef.current
    if (!el) return
    stopGlide()
    drag.current = {
      active: true,
      moved: false,
      startX: e.clientX,
      startScroll: el.scrollLeft,
      lastX: e.clientX,
      lastT: e.timeStamp,
      velocity: 0,
    }
  }

  const onPointerMove = (e: PointerEvent) => {
    const el = trackRef.current
    if (!el || !drag.current.active) return
    const dx = e.clientX - drag.current.startX
    if (!drag.current.moved && Math.abs(dx) < 4) return
    if (!drag.current.moved) {
      drag.current.moved = true
      el.style.scrollSnapType = 'none'
      el.setPointerCapture(e.pointerId)
    }
    // Exponentially-smoothed velocity: responsive to the latest hand speed but
    // not jittery, and a pause before release correctly decays it toward 0.
    const dt = e.timeStamp - drag.current.lastT
    if (dt > 0) {
      const instant = -(e.clientX - drag.current.lastX) / dt
      const blend = Math.min(1, dt / 50)
      drag.current.velocity += (instant - drag.current.velocity) * blend
    }
    drag.current.lastX = e.clientX
    drag.current.lastT = e.timeStamp
    el.scrollLeft = drag.current.startScroll - dx
  }

  const endDrag = () => {
    const el = trackRef.current
    drag.current.active = false
    if (!el) return
    if (!drag.current.moved) {
      // A press that never became a drag (e.g. a click that interrupted a
      // glide) must still hand scroll snapping back to CSS.
      el.style.scrollSnapType = ''
      return
    }
    const maxScroll = el.scrollWidth - el.clientWidth
    // Project where the flick would land under constant deceleration
    // (iOS-style: distance ∝ velocity), then settle on the card edge nearest
    // the projection. A slow release projects ~nowhere and snaps to the
    // nearest edge as before; a flick carries one or more cards further.
    const projected = Math.max(0, Math.min(maxScroll, el.scrollLeft + drag.current.velocity * 180))
    // Candidates in scroll-space (card position relative to the track, not
    // the page — offsetLeft is unreliable here because the track is not the
    // cards' offsetParent). Edges are clamped — cards near the end can't
    // reach the left edge — plus the very end of the track, so a drag near
    // the end settles flush against it instead of pulling back to a card edge.
    const trackLeft = el.getBoundingClientRect().left
    const candidates = (Array.from(el.children) as HTMLElement[]).map((card) =>
      Math.min(card.getBoundingClientRect().left - trackLeft + el.scrollLeft, maxScroll),
    )
    candidates.push(maxScroll)
    let target = 0
    let bestDist = Infinity
    for (const left of candidates) {
      const dist = Math.abs(left - projected)
      if (dist < bestDist) {
        bestDist = dist
        target = left
      }
    }

    // Glide there with an rAF tween whose initial slope matches the release
    // velocity, so the hand-off from drag to animation is seamless. Cubic
    // ease-out — fast launch, long soft landing.
    const from = el.scrollLeft
    const distance = target - from
    if (Math.abs(distance) < 1) {
      el.style.scrollSnapType = ''
      return
    }
    // Duration from velocity: a hard flick gets a longer, more cinematic
    // glide; a nudge settles quickly. Clamped so it never drags on.
    const speed = Math.abs(drag.current.velocity)
    const duration = Math.max(300, Math.min(900, 300 + speed * 250))
    let start: number | null = null
    const tick = (now: number) => {
      if (start == null) start = now
      const t = Math.min(1, (now - start) / duration)
      const eased = 1 - (1 - t) ** 3
      el.scrollLeft = from + distance * eased
      if (t < 1) {
        glide.current = requestAnimationFrame(tick)
      } else {
        glide.current = null
        // Re-enable CSS snap only after the glide settles — flipping it back
        // on mid-flight makes the browser snap instantly and kills the motion.
        el.style.scrollSnapType = ''
      }
    }
    glide.current = requestAnimationFrame(tick)
  }

  return (
    <section className="mb-8">
      <h2 className="mb-3 text-lg font-semibold">Continue watching</h2>
      <div
        ref={trackRef}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={endDrag}
        onPointerCancel={endDrag}
        onClickCapture={(e) => {
          if (drag.current.moved) {
            e.preventDefault()
            e.stopPropagation()
            drag.current.moved = false
          }
        }}
        onDragStart={(e) => e.preventDefault()}
        className="scrollbar-none flex cursor-grab snap-x snap-mandatory gap-4 overflow-x-auto pb-2 select-none active:cursor-grabbing"
      >
        {items.map((item) => (
          <ContinueCard key={item.id} item={item} />
        ))}
      </div>
    </section>
  )
}

function ContinueCard({ item }: { item: ItemSummary }) {
  const [brokenThumb, setBrokenThumb] = useState(false)
  // thumb_url is versioned (?v= appears when the thumbnail lands, changes when
  // it regenerates) — a new URL means the failure no longer applies, so
  // re-seed during render to remount the <img> and retry.
  const [syncedThumbUrl, setSyncedThumbUrl] = useState(item.thumb_url)
  if (item.thumb_url !== syncedThumbUrl) {
    setSyncedThumbUrl(item.thumb_url)
    setBrokenThumb(false)
  }
  const progress = progressPercent(item.progress?.position_s, item.duration_s)
  return (
    <Link
      to={`/watch/${item.id}`}
      state={{ from: '/' }}
      aria-label={`Resume ${item.title}`}
      className="w-56 shrink-0 snap-start rounded-md sm:w-64"
    >
      <div className="bg-inset relative aspect-video overflow-hidden rounded-md">
        {!brokenThumb ? (
          <img
            src={item.thumb_url}
            alt=""
            loading="lazy"
            onError={() => setBrokenThumb(true)}
            className="h-full w-full object-cover"
          />
        ) : (
          <div className="text-tertiary flex h-full w-full items-center justify-center">
            <Clapperboard aria-hidden className="size-8" strokeWidth={1.5} />
          </div>
        )}
        {progress > 0 && (
          <span className="bg-progress-track absolute inset-x-0 bottom-0 h-[3px]">
            <span className="bg-accent-fill block h-full rounded-full" style={{ width: `${progress}%` }} />
          </span>
        )}
      </div>
      <h3 className="mt-2 truncate text-sm font-medium">{item.title}</h3>
    </Link>
  )
}

function PosterCard({
  item,
  isNew,
  justArrived,
  libraryPath,
  collections,
  onToggleCollection,
  onCreateCollectionAndAdd,
  onRequestDelete,
  selectionMode,
  selected,
  onEnterSelection,
  onToggleSelect,
}: {
  item: ItemSummary
  isNew: boolean
  justArrived: boolean
  libraryPath: string
  collections: Collection[]
  onToggleCollection: (item: ItemSummary, collection: Collection, isMember: boolean) => void
  onCreateCollectionAndAdd: (item: ItemSummary, name: string) => void
  onRequestDelete: (item: ItemSummary) => void
  selectionMode: boolean
  selected: boolean
  onEnterSelection: (id: number) => void
  onToggleSelect: (id: number, shiftRange: boolean) => void
}) {
  const navigate = useNavigate()
  const [collectionFilter, setCollectionFilter] = useState('')
  const [brokenThumb, setBrokenThumb] = useState(false)
  // Same pattern as ContinueCard: a changed (versioned) thumb_url invalidates
  // a previous load failure — retry instead of showing the placeholder forever.
  const [syncedThumbUrl, setSyncedThumbUrl] = useState(item.thumb_url)
  if (item.thumb_url !== syncedThumbUrl) {
    setSyncedThumbUrl(item.thumb_url)
    setBrokenThumb(false)
  }
  const progress = item.progress?.completed
    ? 100
    : progressPercent(item.progress?.position_s, item.duration_s)
  const meta = [item.year, formatDuration(item.duration_s)].filter(Boolean).join(' · ')

  // 4.1: long-press (touch) enters selection mode. The press is cancelled by
  // lift/movement; when it fires, the release click is swallowed so it
  // doesn't immediately untoggle the item it just selected.
  const longPressTimer = useRef<number | null>(null)
  const pressOrigin = useRef<{ x: number; y: number } | null>(null)
  const suppressClick = useRef(false)
  useEffect(
    () => () => {
      if (longPressTimer.current != null) window.clearTimeout(longPressTimer.current)
    },
    [],
  )
  const cancelLongPress = () => {
    if (longPressTimer.current != null) {
      window.clearTimeout(longPressTimer.current)
      longPressTimer.current = null
    }
    pressOrigin.current = null
  }

  return (
    <Card
      interactive
      className={[
        'group relative select-none [-webkit-touch-callout:none]',
        selected ? 'ring-2 ring-accent' : '',
        justArrived ? 'animate-[card-enter_var(--duration-slow)_var(--easing-out)]' : '',
      ].join(' ')}
      onPointerDown={(event) => {
        if (event.pointerType === 'mouse' || selectionMode) return
        pressOrigin.current = { x: event.clientX, y: event.clientY }
        longPressTimer.current = window.setTimeout(() => {
          longPressTimer.current = null
          suppressClick.current = true
          onEnterSelection(item.id)
        }, 500)
      }}
      onPointerMove={(event) => {
        const origin = pressOrigin.current
        if (!origin || longPressTimer.current == null) return
        if (Math.hypot(event.clientX - origin.x, event.clientY - origin.y) > 10) cancelLongPress()
      }}
      onPointerUp={cancelLongPress}
      onPointerCancel={cancelLongPress}
      onPointerLeave={cancelLongPress}
      onClickCapture={
        selectionMode
          ? (event) => {
              event.preventDefault()
              event.stopPropagation()
              if (suppressClick.current) {
                suppressClick.current = false
                return
              }
              onToggleSelect(item.id, event.shiftKey)
            }
          : undefined
      }
    >
      {item.available ? (
        <>
          {/* 3.1: the poster plays instantly; the metadata strip below opens
              details. Details also stays reachable via the ⋯ menu. */}
          <Link to={`/watch/${item.id}`} state={{ from: libraryPath }} aria-label={`Play ${item.title}`} className="block">
            <PosterThumb
              item={item}
              isNew={isNew}
              progress={progress}
              brokenThumb={brokenThumb}
              onError={() => setBrokenThumb(true)}
            />
          </Link>
          <Link to={`/items/${item.id}`} state={{ from: libraryPath }} className="block">
            <PosterMeta title={item.title} meta={meta} />
          </Link>
        </>
      ) : (
        <Link to={`/items/${item.id}`} state={{ from: libraryPath }} className="block">
          <PosterThumb
            item={item}
            isNew={isNew}
            progress={progress}
            brokenThumb={brokenThumb}
            onError={() => setBrokenThumb(true)}
          />
          <PosterMeta title={item.title} meta={meta} />
        </Link>
      )}
      {/* 4.1: hover-revealed on desktop (enters selection mode); always
          visible while selecting. In selection mode the card's capture
          handler owns clicks, so this is the visual state only. */}
      <button
        type="button"
        role="checkbox"
        aria-checked={selected}
        aria-label={`Select ${item.title}`}
        onClick={() => {
          if (!selectionMode) onEnterSelection(item.id)
        }}
        className={[
          'absolute top-2 left-2 z-10 flex size-6 cursor-pointer items-center justify-center rounded-sm border transition-opacity duration-[var(--duration-fast)]',
          selected
            ? 'bg-accent-fill text-on-accent border-transparent'
            : 'bg-raised/90 text-primary border-line shadow-raised',
          selectionMode ? 'opacity-100' : 'opacity-0 group-hover:opacity-100 focus-visible:opacity-100',
        ].join(' ')}
      >
        {selected && <Check aria-hidden className="size-4" strokeWidth={2} />}
      </button>
      {!selectionMode && (
      <Menu
        aria-label="Open item menu"
        triggerClassName="bg-raised/90 text-primary border-line absolute top-2 right-2 flex size-9 cursor-pointer items-center justify-center rounded-sm border shadow-raised"
        trigger={<MoreHorizontal aria-hidden className="size-5" strokeWidth={1.75} />}
        onOpenChange={(open) => {
          if (!open) setCollectionFilter('')
        }}
      >
        {({ view, setView }) =>
          view === 'collections' ? (
            <CollectionPicker
              item={item}
              collections={collections}
              filter={collectionFilter}
              setFilter={setCollectionFilter}
              onBack={() => setView(null)}
              onToggleCollection={onToggleCollection}
              onCreateCollectionAndAdd={onCreateCollectionAndAdd}
            />
          ) : (
            <>
              <MenuItem
                icon={<Play className="size-4" strokeWidth={1.75} />}
                disabled={!item.available}
                onSelect={() => navigate(`/watch/${item.id}`, { state: { from: libraryPath } })}
              >
                Play
              </MenuItem>
              <MenuItem
                icon={<Info className="size-4" strokeWidth={1.75} />}
                onSelect={() => navigate(`/items/${item.id}`, { state: { from: libraryPath } })}
              >
                Details
              </MenuItem>
              <MenuItem
                trailing={<ChevronRight className="size-4" strokeWidth={1.75} />}
                closeOnSelect={false}
                onSelect={() => setView('collections')}
              >
                Add to collection…
              </MenuItem>
              <MenuSeparator />
              <MenuItem
                danger
                icon={<Trash2 className="size-4" strokeWidth={1.75} />}
                onSelect={() => onRequestDelete(item)}
              >
                Move to trash
              </MenuItem>
            </>
          )
        }
      </Menu>
      )}
    </Card>
  )
}

function PosterThumb({
  item,
  isNew,
  progress,
  brokenThumb,
  onError,
}: {
  item: ItemSummary
  isNew: boolean
  progress: number
  brokenThumb: boolean
  onError: () => void
}) {
  return (
    <div className="bg-inset relative aspect-video overflow-hidden">
      {!brokenThumb ? (
        <img
          src={item.thumb_url}
          alt=""
          loading="lazy"
          onError={onError}
          className={[
            'h-full w-full object-cover transition-[filter,opacity] duration-[var(--duration-fast)]',
            item.available ? '' : 'opacity-40 grayscale',
          ].join(' ')}
        />
      ) : (
        <div
          className={[
            'flex h-full w-full items-center justify-center',
            item.available ? 'text-tertiary' : 'text-disabled opacity-70',
          ].join(' ')}
        >
          <Clapperboard aria-hidden className="size-10" strokeWidth={1.5} />
        </div>
      )}
      {!item.available && (
        <span className="bg-raised text-secondary border-line absolute top-2 right-12 inline-flex size-8 items-center justify-center rounded-sm border">
          <HardDrive aria-hidden className="size-4" strokeWidth={1.75} />
        </span>
      )}
      {isNew && (
        <span className="bg-accent-fill text-accent-contrast absolute top-2 left-2 rounded-sm px-2 py-1 text-xs font-semibold">
          New
        </span>
      )}
      {progress > 0 && (
        <span className="bg-progress-track absolute inset-x-0 bottom-0 h-[3px]">
          <span className="bg-accent-fill block h-full rounded-full" style={{ width: `${progress}%` }} />
        </span>
      )}
    </div>
  )
}

function PosterMeta({ title, meta }: { title: string; meta: string }) {
  return (
    <div className="p-3">
      <h2 className="truncate text-md font-medium">{title}</h2>
      <p className="truncate text-sm text-secondary">{meta}</p>
    </div>
  )
}

function CollectionPicker({
  item,
  collections,
  filter,
  setFilter,
  onBack,
  onToggleCollection,
  onCreateCollectionAndAdd,
}: {
  item: ItemSummary
  collections: Collection[]
  filter: string
  setFilter: (value: string) => void
  onBack: () => void
  onToggleCollection: (item: ItemSummary, collection: Collection, isMember: boolean) => void
  onCreateCollectionAndAdd: (item: ItemSummary, name: string) => void
}) {
  return (
    <div className="w-56">
      <MenuItem
        icon={<ChevronLeft className="size-4" strokeWidth={1.75} />}
        closeOnSelect={false}
        onSelect={onBack}
      >
        Back
      </MenuItem>
      <CollectionListPicker
        collections={collections}
        filter={filter}
        setFilter={setFilter}
        keepOpenOnPick
        isChecked={(collection) => item.collection_ids.includes(collection.id)}
        onPick={(collection) =>
          onToggleCollection(item, collection, item.collection_ids.includes(collection.id))
        }
        onCreate={(name) => onCreateCollectionAndAdd(item, name)}
      />
    </div>
  )
}

/** Filterable collection list with a create row — shared by the per-item
    picker (checkmarks, stays open to toggle) and the batch action bar
    (pick-once, closes). */
function CollectionListPicker({
  collections,
  filter,
  setFilter,
  isChecked,
  onPick,
  onCreate,
  keepOpenOnPick = false,
}: {
  collections: Collection[]
  filter: string
  setFilter: (value: string) => void
  isChecked?: (collection: Collection) => boolean
  onPick: (collection: Collection) => void
  onCreate: (name: string) => void
  keepOpenOnPick?: boolean
}) {
  const trimmed = filter.trim()
  const matches = collections.filter((collection) =>
    collection.name.toLowerCase().includes(trimmed.toLowerCase()),
  )
  const canCreate =
    trimmed.length > 0 &&
    !collections.some((collection) => collection.name.toLowerCase() === trimmed.toLowerCase())

  return (
    <div className="min-w-56">
      <div className="p-1">
        <Input
          aria-label="Filter collections"
          placeholder="Filter"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
        />
      </div>
      <div className="max-h-56 overflow-y-auto">
        {matches.map((collection) => (
          <MenuItem
            key={collection.id}
            checked={isChecked?.(collection)}
            closeOnSelect={!keepOpenOnPick}
            onSelect={() => onPick(collection)}
          >
            {collection.name}
          </MenuItem>
        ))}
        {matches.length === 0 && !canCreate && (
          <p className="text-tertiary px-3 py-2 text-sm">No collections</p>
        )}
      </div>
      {canCreate && (
        <MenuItem
          icon={<Plus className="size-4" strokeWidth={1.75} />}
          closeOnSelect={!keepOpenOnPick}
          onSelect={() => {
            onCreate(trimmed)
            setFilter('')
          }}
        >
          New collection "{trimmed}"
        </MenuItem>
      )}
    </div>
  )
}

function hasFiles(event: DragEvent<HTMLElement>): boolean {
  return Array.from(event.dataTransfer.types).includes('Files')
}

function EmptyMessage({ text }: { text: string }) {
  return (
    <div className="flex flex-col items-center gap-3 py-24 text-center">
      <Clapperboard aria-hidden className="text-tertiary size-10" strokeWidth={1.75} />
      <p className="text-md">{text}</p>
    </div>
  )
}

