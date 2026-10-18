import { useEffect, useRef, useState, type DragEvent } from 'react'
import { Link, useLocation, useSearchParams } from 'react-router'
import {
  ChevronDown,
  Clapperboard,
  Layers,
  ListChecks,
  Plus,
  Search,
  Settings,
  Trash2,
  Upload,
  X,
} from 'lucide-react'
import {
  useAddItemToCollection,
  useAddShowToCollection,
  useCollections,
  useContinueWatching,
  useCreateCollection,
  useDeleteItem,
  useHealth,
  useLibraryEntries,
  useRestoreItem,
  useTrashShow,
  type LibraryEntryFilters,
} from '../../api/queries.ts'
import { useLiveItems } from '../../api/sse.tsx'
import { useUploads } from '../upload/UploadProvider.tsx'
import type { Collection, LibraryEntry } from '../../api/types.ts'
import { useCardStyle } from '../../theme/cardStyle.ts'
import { isRecentlyCreated } from '../../lib/format.ts'
import { makeRunOrToast } from '../../lib/mutationFeedback.ts'
import {
  libraryParamUpdates,
  parseLibraryParams,
  withParamUpdates,
  type LibraryType,
} from '../../lib/searchParams.ts'
import { ContinueWatchingRow } from './ContinueWatchingRow.tsx'
import { ItemCard } from './ItemCard.tsx'
import { LibraryTypeTabs } from './LibraryTypeTabs.tsx'
import { ShowCard } from './ShowCard.tsx'
import { CollectionListPicker } from './CollectionListPicker.tsx'
import {
  Button,
  Dialog,
  IconButton,
  Input,
  Menu,
  MenuItem,
  Skeleton,
  useToast,
} from '../../ui/index.ts'

const SORT_OPTIONS: { value: NonNullable<LibraryEntryFilters['sort']>; label: string }[] = [
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
  const type = urlState.type

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

  const setSort = (next: LibraryEntryFilters['sort']) =>
    setSearchParams(
      (prev) =>
        withParamUpdates(
          prev,
          libraryParamUpdates({ q: urlState.q, sort: next ?? 'added', collectionID, uncollected, type }),
        ),
      { replace: true },
    )
  const setType = (next: LibraryType) =>
    setSearchParams(
      (prev) =>
        withParamUpdates(prev, libraryParamUpdates({ q: urlState.q, sort, collectionID, uncollected, type: next })),
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
            type,
          }),
        ),
      { replace: true },
    )
  const pillClass = (active: boolean) =>
    [
      'border-line rounded-full border px-3 py-1.5 text-sm',
      active ? 'bg-accent-fill text-on-accent' : 'bg-surface text-primary hover:bg-raised',
    ].join(' ')

  // The item/watch pages send the user back here via router state — built
  // from the live `query` (not the debounced `urlState.q`) so a poster click
  // right after typing still carries the not-yet-committed search text.
  const libraryPath = (() => {
    const qs = withParamUpdates(
      searchParams,
      libraryParamUpdates({ q: query, sort, collectionID, uncollected, type }),
    ).toString()
    return qs ? `/?${qs}` : '/'
  })()

  // React Query serializes the query key by value, so this doesn't need to
  // be a stable reference across renders.
  const filters: LibraryEntryFilters = {
    q: urlState.q,
    sort,
    type: type || undefined,
    collection_id: collectionID,
    uncollected,
  }
  const library = useLibraryEntries(filters)
  const collections = useCollections()
  const addToCollection = useAddItemToCollection()
  const addShowToCollection = useAddShowToCollection()
  const createCollection = useCreateCollection()
  const deleteItem = useDeleteItem()
  const trashShow = useTrashShow()
  const restoreItem = useRestoreItem()
  const liveItems = useLiveItems()
  const uploads = useUploads()
  const cardStyle = useCardStyle()
  const { toast } = useToast()
  const entries = library.data?.pages.flatMap((page) => page.entries) ?? []
  // "Continue watching" is its own server-side query so it covers the whole
  // catalog. A search or collection filter narrows the intent and hides it;
  // the All · Shows · Movies tabs sit below it and scope only the grid.
  // Capped: it's a shortcut row, not a second library.
  const showContinueWatching = !urlState.q && collectionID == null && !uncollected
  const continueQuery = useContinueWatching(showContinueWatching)
  const continueWatching = showContinueWatching
    ? (continueQuery.data?.entries ?? [])
        .filter((entry) => (entry.kind === 'show' ? entry.up_next?.available : entry.available))
        .slice(0, 8)
    : []
  const sentinelRef = useRef<HTMLDivElement | null>(null)
  const fileInputRef = useRef<HTMLInputElement | null>(null)
  const dragDepth = useRef(0)
  const [draggingFiles, setDraggingFiles] = useState(false)

  // 4.1: selection mode. null = off; a Set (possibly empty) = active.
  // Per-visit state by design — never in the URL; exiting clears it.
  // Keys are entryKey()s, so a show and an item sharing an id stay distinct.
  const [selection, setSelection] = useState<Set<string> | null>(null)
  const selectionAnchor = useRef<string | null>(null)
  const [batchDeleteOpen, setBatchDeleteOpen] = useState(false)
  const [batchFilter, setBatchFilter] = useState('')
  const selectionActive = selection != null

  // Entered from the toolbar (no item yet) or by long-pressing a card (that
  // item pre-selected). Delete / add-to-collection then run from the action bar.
  const startSelection = () => {
    selectionAnchor.current = null
    setSelection(new Set())
  }
  const enterSelection = (key: string) => {
    selectionAnchor.current = key
    setSelection(new Set([key]))
  }
  const exitSelection = () => {
    selectionAnchor.current = null
    setSelection(null)
    setBatchDeleteOpen(false)
  }
  const toggleSelected = (key: string, shiftRange: boolean) => {
    setSelection((prev) => {
      if (!prev) return prev
      const next = new Set(prev)
      const anchor = selectionAnchor.current
      if (shiftRange && anchor != null) {
        const keys = entries.map(entryKey)
        const from = keys.indexOf(anchor)
        const to = keys.indexOf(key)
        if (from !== -1 && to !== -1) {
          for (let i = Math.min(from, to); i <= Math.max(from, to); i++) next.add(keys[i])
          return next
        }
      }
      if (next.has(key)) next.delete(key)
      else next.add(key)
      selectionAnchor.current = key
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
    entries.length,
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

  // Batch actions act on the intersection of the selection and the currently
  // loaded entries — keys that vanished (or arrived via SSE untracked) are
  // ignored rather than erroring.
  const selectedEntries = () => entries.filter((entry) => selection?.has(entryKey(entry)))

  const addEntry = (collectionID: number, entry: LibraryEntry) =>
    entry.kind === 'show'
      ? addShowToCollection.mutateAsync({ collectionID, showID: entry.id })
      : addToCollection.mutateAsync({ collectionID, itemID: entry.id })

  const onBatchAddToCollection = async (collection: Collection) => {
    const targets = selectedEntries()
    const ok = await runOrToast(async () => {
      for (const entry of targets) {
        if (entry.collection_ids.includes(collection.id)) continue
        await addEntry(collection.id, entry)
      }
    }, "Couldn't add to collection")
    if (!ok) return
    toast({ message: `Added ${entriesLabel(targets)} to ${collection.name}` })
    exitSelection()
  }

  const onBatchCreateCollectionAndAdd = async (name: string) => {
    const targets = selectedEntries()
    const ok = await runOrToast(async () => {
      const collection = await createCollection.mutateAsync(name)
      for (const entry of targets) await addEntry(collection.id, entry)
    }, "Couldn't create collection")
    if (!ok) return
    toast({ message: `Added ${entriesLabel(targets)} to ${name}` })
    exitSelection()
  }

  // A show goes to the trash with every live episode; undo restores each one.
  const onBatchDelete = async () => {
    const targets = selectedEntries()
    const trashed: LibraryEntry[] = []
    const restoreIDs: number[] = []
    const ok = await runOrToast(async () => {
      for (const entry of targets) {
        if (entry.kind === 'show') {
          const { item_ids } = await trashShow.mutateAsync(entry.id)
          restoreIDs.push(...item_ids)
        } else {
          await deleteItem.mutateAsync(entry.id)
          restoreIDs.push(entry.id)
        }
        trashed.push(entry)
      }
    }, "Couldn't move to trash")
    setBatchDeleteOpen(false)
    if (trashed.length > 0) {
      toast({
        message: `${entriesLabel(trashed)} moved to trash`,
        action: {
          label: 'Undo',
          onClick: () => {
            void runOrToast(async () => {
              for (const id of restoreIDs) await restoreItem.mutateAsync(id)
            }, "Couldn't restore items")
          },
        },
      })
    }
    if (ok) exitSelection()
  }

  const selectedNow = selectedEntries()
  const libraryEmpty =
    !library.isPending &&
    entries.length === 0 &&
    !type &&
    !urlState.q &&
    collectionID == null &&
    !uncollected

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

      {library.isError && <EmptyMessage text="Can't load the library" />}

      {continueWatching.length > 0 && (
        <ContinueWatchingRow entries={continueWatching} cardStyle={cardStyle} libraryPath={libraryPath} />
      )}

      {!library.isError && !libraryEmpty && (
        <div className="mb-4 flex items-center justify-between text-sm text-secondary">
          <LibraryTypeTabs value={type} onChange={setType} />
          <div className="flex items-center gap-4">
            {urlState.q && <span className="hidden sm:inline">Search results for "{urlState.q}"</span>}
            {!selectionActive && entries.length > 0 && (
              <button
                type="button"
                onClick={startSelection}
                className="text-secondary hover:text-primary -my-1 inline-flex items-center gap-1.5 py-1"
              >
                <ListChecks aria-hidden className="size-4" strokeWidth={1.75} />
                Select
              </button>
            )}
          </div>
        </div>
      )}

      {library.isPending && <SkeletonGrid count={12} />}

      {!library.isPending && !library.isError && entries.length === 0 && (
        <EmptyMessage
          text={
            urlState.q
              ? 'No matching videos'
              : type === 'show'
                ? 'No shows'
                : type === 'movie'
                  ? 'No movies'
                  : uncollected
                    ? 'Everything is in a collection'
                    : collectionID != null
                      ? 'Nothing in this collection'
                      : 'Library is empty'
          }
        />
      )}

      {entries.length > 0 && (
        <>
          <div data-library-grid className={GRID}>
            {entries.map((entry) => {
              const key = entryKey(entry)
              const cardSelection = {
                active: selectionActive,
                selected: selection?.has(key) ?? false,
                onEnter: () => enterSelection(key),
                onToggle: (shiftRange: boolean) => toggleSelected(key, shiftRange),
              }
              return entry.kind === 'show' ? (
                <ShowCard
                  key={key}
                  show={entry}
                  cardStyle={cardStyle}
                  libraryPath={libraryPath}
                  selection={cardSelection}
                />
              ) : (
                <ItemCard
                  key={key}
                  item={entry}
                  cardStyle={cardStyle}
                  isNew={isRecentlyCreated(entry.created_at)}
                  justArrived={liveItems.hasJustArrived(entry.id)}
                  libraryPath={libraryPath}
                  selection={cardSelection}
                />
              )
            })}
          </div>
          <div ref={sentinelRef} className="h-12" aria-hidden />
          {library.isFetchingNextPage && <SkeletonGrid count={6} />}
        </>
      )}

      {/* 4.1: selection action bar — below toasts (--z-toast) so feedback
          stays visible above it. */}
      {selection && (
        <div className="fixed inset-x-0 bottom-4 z-[var(--z-tray)] flex justify-center px-4">
          <div className="bg-raised border-line shadow-overlay flex flex-wrap items-center gap-2 rounded-lg border px-4 py-2">
            <span className="text-sm text-secondary tabular mr-2" role="status">
              {selection.size} selected
            </span>
            <Menu
              aria-label="Add selected to collection"
              disabled={selection.size === 0}
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
            <Button
              variant="danger"
              pending={deleteItem.isPending || trashShow.isPending}
              onClick={() => void onBatchDelete()}
            >
              <Trash2 aria-hidden className="size-4" strokeWidth={1.75} />
              Move to trash
            </Button>
          </>
        }
      >
        <p className="text-secondary">
          {entriesLabel(selectedNow)} will move to the root's trash folder.
          {selectedNow.some((entry) => entry.kind === 'show') && ' Shows go with all their episodes.'}
        </p>
      </Dialog>
    </main>
  )
}

const GRID =
  'grid grid-cols-[repeat(auto-fill,minmax(160px,1fr))] gap-4 sm:grid-cols-[repeat(auto-fill,minmax(240px,1fr))]'

function SkeletonGrid({ count }: { count: number }) {
  return (
    <div className={GRID}>
      {Array.from({ length: count }, (_, i) => (
        <Skeleton key={i} className="aspect-video" />
      ))}
    </div>
  )
}

function entryKey(entry: LibraryEntry): string {
  return `${entry.kind}:${entry.id}`
}

/** `1 show and 2 items`, `3 items`, `1 show`. */
function entriesLabel(entries: LibraryEntry[]): string {
  const shows = entries.filter((entry) => entry.kind === 'show').length
  const items = entries.length - shows
  const parts = [
    shows > 0 ? (shows === 1 ? '1 show' : `${shows} shows`) : '',
    items > 0 || shows === 0 ? (items === 1 ? '1 item' : `${items} items`) : '',
  ].filter(Boolean)
  return parts.join(' and ')
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

