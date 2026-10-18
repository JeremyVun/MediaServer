import { useEffect, useMemo, useState, type DragEvent, type FormEvent } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { ArrowLeft, Clapperboard, Edit3, GripVertical, Plus, Save, SearchX, Trash2, X } from 'lucide-react'
import {
  useCollections,
  useCreateCollection,
  useDeleteCollection,
  useLibraryEntries,
  useRemoveItemFromCollection,
  useRemoveShowFromCollection,
  useRenameCollection,
  useReorderCollectionEntries,
} from '../../api/queries.ts'
import type { Collection, LibraryEntry } from '../../api/types.ts'
import { useCardStyle } from '../../theme/cardStyle.ts'
import { ItemCard } from '../library/ItemCard.tsx'
import { ShowCard } from '../library/ShowCard.tsx'
import { makeRunOrToast, mutationErrorMessage } from '../../lib/mutationFeedback.ts'
import { Button, Card, Dialog, IconButton, Input, Skeleton, useToast } from '../../ui/index.ts'

export function CollectionsPage() {
  const params = useParams()
  const selectedID = params.id ? Number(params.id) : null
  const collections = useCollections()
  const selected = collections.data?.find((collection) => collection.id === selectedID) ?? null
  const [createOpen, setCreateOpen] = useState(false)

  // 4.4: a deep link to /collections/:id needs three distinct states —
  // pending (skeleton, not the index), loaded-but-missing (not-found empty
  // state), and loaded-and-found (the detail view below).
  if (selectedID != null) {
    if (collections.isPending) return <CollectionDetailSkeleton />
    if (collections.isError) return <CollectionDetailError />
    if (!selected) return <CollectionNotFound />
  }

  if (selectedID && selected) {
    return <CollectionDetail collection={selected} />
  }

  return (
    <main className="mx-auto max-w-6xl px-4 py-6 sm:px-6 lg:px-8">
      <header className="mb-6 flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="flex items-start gap-3">
          <Link
            to="/"
            aria-label="Back to library"
            className="bg-surface border-line text-primary hover:bg-raised inline-flex size-11 shrink-0 items-center justify-center rounded-md border"
          >
            <ArrowLeft aria-hidden className="size-5" strokeWidth={1.75} />
          </Link>
          <div>
            <h1 className="text-xl font-semibold">Collections</h1>
            <p className="text-sm text-secondary">
              {collections.data ? `${collections.data.length} collections` : 'Loading'}
            </p>
          </div>
        </div>
        <Button variant="primary" touch onClick={() => setCreateOpen(true)}>
          <Plus aria-hidden className="size-4" strokeWidth={1.75} />
          New collection
        </Button>
      </header>

      {collections.isPending && (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {Array.from({ length: 6 }, (_, i) => (
            <Skeleton key={i} className="h-44" />
          ))}
        </div>
      )}
      {collections.isError && <EmptyCollections text="Can't load collections" />}
      {collections.data?.length === 0 && <EmptyCollections text="No collections" />}
      {collections.data && collections.data.length > 0 && (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {collections.data.map((collection) => (
            <CollectionCard key={collection.id} collection={collection} />
          ))}
        </div>
      )}

      {createOpen && <CollectionNameDialog title="New collection" onClose={() => setCreateOpen(false)} />}
    </main>
  )
}

function CollectionDetailSkeleton() {
  return (
    <main className="mx-auto max-w-6xl px-4 py-6 sm:px-6 lg:px-8">
      <header className="mb-6 flex items-start gap-3">
        <Link
          to="/collections"
          aria-label="Back to collections"
          className="bg-surface border-line text-primary hover:bg-raised inline-flex size-11 shrink-0 items-center justify-center rounded-md border"
        >
          <ArrowLeft aria-hidden className="size-5" strokeWidth={1.75} />
        </Link>
        <div className="min-w-0 flex-1 space-y-2">
          <Skeleton className="h-7 w-48" />
          <Skeleton className="h-4 w-24" />
        </div>
      </header>
      <div className="grid grid-cols-[repeat(auto-fill,minmax(150px,1fr))] gap-4">
        {Array.from({ length: 8 }, (_, i) => (
          <Skeleton key={i} className="aspect-[2/3]" />
        ))}
      </div>
    </main>
  )
}

function CollectionNotFound() {
  return (
    <main className="mx-auto max-w-6xl px-4 py-6 sm:px-6 lg:px-8">
      <div className="flex flex-col items-center gap-3 py-24 text-center">
        <SearchX aria-hidden className="text-tertiary size-10" strokeWidth={1.75} />
        <p className="text-md">This collection doesn't exist.</p>
        <Link to="/collections" className="text-accent hover:text-accent-hover text-sm font-semibold">
          Back to collections
        </Link>
      </div>
    </main>
  )
}

function CollectionDetailError() {
  return (
    <main className="mx-auto max-w-6xl px-4 py-6 sm:px-6 lg:px-8">
      <div className="flex flex-col items-center gap-3 py-24 text-center">
        <SearchX aria-hidden className="text-tertiary size-10" strokeWidth={1.75} />
        <p className="text-md">Can't load this collection.</p>
        <Link to="/collections" className="text-accent hover:text-accent-hover text-sm font-semibold">
          Back to collections
        </Link>
      </div>
    </main>
  )
}

function CollectionCard({ collection }: { collection: Collection }) {
  return (
    <Card interactive className="overflow-hidden">
      <Link to={`/collections/${collection.id}`} className="block">
        <Montage collection={collection} />
        <div className="p-4">
          <h2 className="truncate text-md font-semibold">{collection.name}</h2>
          <p className="text-sm text-secondary">{collection.item_count} items</p>
        </div>
      </Link>
    </Card>
  )
}

function CollectionDetail({ collection }: { collection: Collection }) {
  const navigate = useNavigate()
  const { toast } = useToast()
  const cardStyle = useCardStyle()
  const entriesQuery = useLibraryEntries({ q: '', collection_id: collection.id })
  const deleteCollection = useDeleteCollection()
  const reorder = useReorderCollectionEntries()
  const removeItem = useRemoveItemFromCollection()
  const removeShow = useRemoveShowFromCollection()
  const runOrToast = makeRunOrToast(toast)
  const [renameOpen, setRenameOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [draggedKey, setDraggedKey] = useState<string | null>(null)
  const entries = useMemo(
    () => entriesQuery.data?.pages.flatMap((page) => page.entries) ?? [],
    [entriesQuery.data],
  )
  const [orderKeys, setOrderKeys] = useState<string[]>([])
  const ordered = useMemo(() => {
    if (!sameKeySet(entries.map(entryKey), orderKeys)) return entries
    const byKey = new Map(entries.map((entry) => [entryKey(entry), entry]))
    return orderKeys.flatMap((key) => {
      const entry = byKey.get(key)
      return entry ? [entry] : []
    })
  }, [entries, orderKeys])
  const backTo = `/collections/${collection.id}`

  // A reorder must name every member, so the whole collection is loaded.
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = entriesQuery
  useEffect(() => {
    if (hasNextPage && !isFetchingNextPage) void fetchNextPage()
  }, [hasNextPage, isFetchingNextPage, fetchNextPage])

  const commitOrder = (next: LibraryEntry[]) => {
    if (hasNextPage) return
    setOrderKeys(next.map(entryKey))
    reorder.mutate(
      {
        collectionID: collection.id,
        entries: next.map((entry) => (entry.kind === 'show' ? { show_id: entry.id } : { item_id: entry.id })),
      },
      { onError: (error) => toast({ message: mutationErrorMessage(error, "Couldn't reorder collection") }) },
    )
  }

  const moveTo = (key: string, to: number) => {
    const from = ordered.findIndex((entry) => entryKey(entry) === key)
    if (from < 0 || to < 0 || to >= ordered.length || from === to) return
    const next = [...ordered]
    const [entry] = next.splice(from, 1)
    next.splice(to, 0, entry)
    commitOrder(next)
  }

  const onDrop = (targetKey: string) => {
    if (draggedKey == null || draggedKey === targetKey) return
    setDraggedKey(null)
    moveTo(draggedKey, ordered.findIndex((entry) => entryKey(entry) === targetKey))
  }

  const remove = (entry: LibraryEntry) =>
    void runOrToast(
      () =>
        entry.kind === 'show'
          ? removeShow.mutateAsync({ collectionID: collection.id, showID: entry.id })
          : removeItem.mutateAsync({ collectionID: collection.id, itemID: entry.id }),
      "Couldn't update collection",
    )

  // 4.3: confirm before deleting — items stay in the library either way, but
  // the collection itself is gone for good (no restore endpoint exists in
  // SPEC-API.md, so there's no undo to offer here, unlike item trash).
  const onDelete = async () => {
    const ok = await runOrToast(() => deleteCollection.mutateAsync(collection.id), "Couldn't delete collection")
    if (!ok) return
    setDeleteOpen(false)
    toast({ message: `${collection.name} deleted` })
    navigate('/collections')
  }

  return (
    <main className="mx-auto max-w-6xl px-4 py-6 sm:px-6 lg:px-8">
      <header className="mb-6 flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="flex items-start gap-3">
          <Link
            to="/collections"
            aria-label="Back to collections"
            className="bg-surface border-line text-primary hover:bg-raised inline-flex size-11 shrink-0 items-center justify-center rounded-md border"
          >
            <ArrowLeft aria-hidden className="size-5" strokeWidth={1.75} />
          </Link>
          <div>
            <h1 className="text-xl font-semibold">{collection.name}</h1>
            <p className="text-sm text-secondary">
              {entriesQuery.isSuccess ? entriesLabel(entries) : `${collection.item_count} items`}
            </p>
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button onClick={() => setRenameOpen(true)}>
            <Edit3 aria-hidden className="size-4" strokeWidth={1.75} />
            Rename
          </Button>
          <Button variant="danger" onClick={() => setDeleteOpen(true)}>
            <Trash2 aria-hidden className="size-4" strokeWidth={1.75} />
            Delete
          </Button>
        </div>
      </header>

      {entriesQuery.isPending && (
        <div className={GRID}>
          {Array.from({ length: 8 }, (_, i) => (
            <Skeleton key={i} className="aspect-video" />
          ))}
        </div>
      )}
      {entriesQuery.isError && <EmptyCollections text="Can't load this collection" />}
      {entriesQuery.isSuccess && ordered.length === 0 && <EmptyCollections text="Collection is empty" />}
      {ordered.length > 0 && (
        <div data-collection-grid className={GRID}>
          {ordered.map((entry, index) => {
            const key = entryKey(entry)
            const footer = (
              <ReorderBar
                title={entry.title}
                first={index === 0}
                last={index === ordered.length - 1}
                onMove={(delta) => moveTo(key, index + delta)}
                onRemove={() => remove(entry)}
              />
            )
            return (
              <div
                key={key}
                draggable
                onDragStart={() => setDraggedKey(key)}
                onDragOver={(event: DragEvent) => event.preventDefault()}
                onDrop={() => onDrop(key)}
              >
                {entry.kind === 'show' ? (
                  <ShowCard show={entry} cardStyle={cardStyle} libraryPath={backTo} footer={footer} />
                ) : (
                  <ItemCard item={entry} cardStyle={cardStyle} libraryPath={backTo} footer={footer} />
                )}
              </div>
            )
          })}
        </div>
      )}

      {renameOpen && (
        <CollectionNameDialog
          title="Rename collection"
          collection={collection}
          onClose={() => setRenameOpen(false)}
        />
      )}

      <Dialog
        open={deleteOpen}
        onClose={() => setDeleteOpen(false)}
        title={`Delete ${collection.name}?`}
        footer={
          <>
            <Button variant="ghost" onClick={() => setDeleteOpen(false)}>
              Cancel
            </Button>
            <Button variant="danger" pending={deleteCollection.isPending} onClick={() => void onDelete()}>
              <Trash2 aria-hidden className="size-4" strokeWidth={1.75} />
              Delete
            </Button>
          </>
        }
      >
        <p className="text-secondary">Items stay in the library.</p>
      </Dialog>
    </main>
  )
}

function ReorderBar({
  title,
  first,
  last,
  onMove,
  onRemove,
}: {
  title: string
  first: boolean
  last: boolean
  onMove: (delta: number) => void
  onRemove: () => void
}) {
  return (
    <div className="border-line flex items-center justify-between border-t px-2 py-2">
      <GripVertical aria-hidden className="text-tertiary size-4" strokeWidth={1.75} />
      <div className="flex gap-1">
        <IconButton aria-label="Move earlier" disabled={first} onClick={() => onMove(-1)}>
          <ArrowLeft aria-hidden className="size-4 rotate-90" strokeWidth={1.75} />
        </IconButton>
        <IconButton aria-label="Move later" disabled={last} onClick={() => onMove(1)}>
          <ArrowLeft aria-hidden className="size-4 -rotate-90" strokeWidth={1.75} />
        </IconButton>
        <IconButton aria-label={`Remove ${title} from collection`} onClick={onRemove}>
          <X aria-hidden className="size-4" strokeWidth={1.75} />
        </IconButton>
      </div>
    </div>
  )
}

function CollectionNameDialog({
  title,
  collection,
  onClose,
}: {
  title: string
  collection?: Collection
  onClose: () => void
}) {
  const create = useCreateCollection()
  const rename = useRenameCollection()
  const { toast } = useToast()
  const runOrToast = makeRunOrToast(toast)
  const [name, setName] = useState(collection?.name ?? '')

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    const trimmed = name.trim()
    if (!trimmed) return
    const ok = await runOrToast(
      () =>
        collection
          ? rename.mutateAsync({ id: collection.id, name: trimmed })
          : create.mutateAsync(trimmed),
      collection ? "Couldn't rename collection" : "Couldn't create collection",
    )
    if (!ok) return
    onClose()
  }

  return (
    <Dialog
      open
      onClose={onClose}
      title={title}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="submit"
            form="collection-name-form"
            variant="primary"
            pending={create.isPending || rename.isPending}
          >
            <Save aria-hidden className="size-4" strokeWidth={1.75} />
            Save
          </Button>
        </>
      }
    >
      <form id="collection-name-form" onSubmit={submit} className="space-y-2">
        <label className="block text-sm font-medium" htmlFor="collection-name">
          Name
        </label>
        <Input id="collection-name" value={name} onChange={(event) => setName(event.target.value)} />
      </form>
    </Dialog>
  )
}

function Montage({ collection }: { collection: Collection }) {
  return (
    <div className="bg-inset grid aspect-[2/1] grid-cols-2 grid-rows-2 overflow-hidden">
      {Array.from({ length: 4 }, (_, index) => {
        const url = collection.thumb_urls[index]
        return url ? (
          <img key={url} src={url} alt="" loading="lazy" className="h-full w-full object-cover" />
        ) : (
          <div key={index} className="flex items-center justify-center text-tertiary">
            <Clapperboard aria-hidden className="size-7" strokeWidth={1.5} />
          </div>
        )
      })}
    </div>
  )
}

function EmptyCollections({ text }: { text: string }) {
  return (
    <div className="flex flex-col items-center gap-3 py-20 text-center">
      <Clapperboard aria-hidden className="text-tertiary size-10" strokeWidth={1.75} />
      <p className="text-md">{text}</p>
    </div>
  )
}

const GRID =
  'grid grid-cols-[repeat(auto-fill,minmax(160px,1fr))] gap-4 sm:grid-cols-[repeat(auto-fill,minmax(240px,1fr))]'

function entryKey(entry: LibraryEntry): string {
  return `${entry.kind}:${entry.id}`
}

/** `1 show and 2 items`, `3 items`, `1 show`. */
function entriesLabel(entries: LibraryEntry[]): string {
  const shows = entries.filter((entry) => entry.kind === 'show').length
  const items = entries.length - shows
  return [
    shows > 0 ? (shows === 1 ? '1 show' : `${shows} shows`) : '',
    items > 0 || shows === 0 ? (items === 1 ? '1 item' : `${items} items`) : '',
  ]
    .filter(Boolean)
    .join(' and ')
}

function sameKeySet(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false
  const set = new Set(a)
  return b.every((key) => set.has(key))
}
