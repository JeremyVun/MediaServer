import { useMemo, useState, type DragEvent, type FormEvent } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import {
  ArrowLeft,
  Clapperboard,
  Edit3,
  GripVertical,
  Plus,
  Save,
  SearchX,
  Trash2,
} from 'lucide-react'
import {
  useCollections,
  useCreateCollection,
  useDeleteCollection,
  useLibraryItems,
  useRenameCollection,
  useReorderCollection,
} from '../../api/queries.ts'
import type { Collection, ItemSummary } from '../../api/types.ts'
import { formatDuration } from '../../lib/format.ts'
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
  const itemsQuery = useLibraryItems({ q: '', collection_id: collection.id })
  const deleteCollection = useDeleteCollection()
  const reorder = useReorderCollection()
  const runOrToast = makeRunOrToast(toast)
  const [renameOpen, setRenameOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [draggedID, setDraggedID] = useState<number | null>(null)
  const items = useMemo(() => itemsQuery.data?.pages.flatMap((page) => page.items) ?? [], [itemsQuery.data])
  const [orderIDs, setOrderIDs] = useState<number[]>([])
  const ordered = useMemo(() => {
    const itemIDs = items.map((item) => item.id)
    if (!sameNumberSet(itemIDs, orderIDs)) return items
    const byID = new Map(items.map((item) => [item.id, item]))
    return orderIDs.flatMap((id) => {
      const item = byID.get(id)
      return item ? [item] : []
    })
  }, [items, orderIDs])

  const commitOrder = (next: ItemSummary[]) => {
    setOrderIDs(next.map((item) => item.id))
    reorder.mutate(
      { collectionID: collection.id, itemIDs: next.map((item) => item.id) },
      { onError: (error) => toast({ message: mutationErrorMessage(error, "Couldn't reorder collection") }) },
    )
  }

  const moveItem = (itemID: number, delta: number) => {
    const index = ordered.findIndex((item) => item.id === itemID)
    const nextIndex = index + delta
    if (index < 0 || nextIndex < 0 || nextIndex >= ordered.length) return
    const next = [...ordered]
    const [item] = next.splice(index, 1)
    next.splice(nextIndex, 0, item)
    commitOrder(next)
  }

  const onDrop = (targetID: number) => {
    if (draggedID == null || draggedID === targetID) return
    const from = ordered.findIndex((item) => item.id === draggedID)
    const to = ordered.findIndex((item) => item.id === targetID)
    if (from < 0 || to < 0) return
    const next = [...ordered]
    const [item] = next.splice(from, 1)
    next.splice(to, 0, item)
    setDraggedID(null)
    commitOrder(next)
  }

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
            <p className="text-sm text-secondary">{collection.item_count} items</p>
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

      {itemsQuery.isPending && (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(160px,1fr))] gap-4 sm:grid-cols-[repeat(auto-fill,minmax(240px,1fr))]">
          {Array.from({ length: 8 }, (_, i) => (
            <Skeleton key={i} className="aspect-video" />
          ))}
        </div>
      )}
      {itemsQuery.isError && <EmptyCollections text="Can't load this collection" />}
      {!itemsQuery.isPending && ordered.length === 0 && <EmptyCollections text="Collection is empty" />}
      {ordered.length > 0 && (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(160px,1fr))] gap-4 sm:grid-cols-[repeat(auto-fill,minmax(240px,1fr))]">
          {ordered.map((item, index) => (
            <CollectionItemCard
              key={item.id}
              item={item}
              backTo={`/collections/${collection.id}`}
              first={index === 0}
              last={index === ordered.length - 1}
              onMove={moveItem}
              onDragStart={() => setDraggedID(item.id)}
              onDrop={() => onDrop(item.id)}
            />
          ))}
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

function CollectionItemCard({
  item,
  backTo,
  first,
  last,
  onMove,
  onDragStart,
  onDrop,
}: {
  item: ItemSummary
  backTo: string
  first: boolean
  last: boolean
  onMove: (itemID: number, delta: number) => void
  onDragStart: () => void
  onDrop: () => void
}) {
  const [broken, setBroken] = useState(false)
  const meta = [item.year, formatDuration(item.duration_s)].filter(Boolean).join(' · ')
  const thumb = (
    <div className="bg-inset aspect-video">
      {!broken ? (
        <img
          src={item.thumb_url}
          alt=""
          loading="lazy"
          onError={() => setBroken(true)}
          className="h-full w-full object-cover"
        />
      ) : (
        <div className="flex h-full w-full items-center justify-center text-tertiary">
          <Clapperboard aria-hidden className="size-10" strokeWidth={1.5} />
        </div>
      )}
    </div>
  )
  const details = (
    <div className="p-3">
      <h2 className="truncate text-md font-medium">{item.title}</h2>
      <p className="truncate text-sm text-secondary">{meta}</p>
    </div>
  )
  return (
    <Card
      interactive
      draggable
      onDragStart={onDragStart}
      onDragOver={(event: DragEvent) => event.preventDefault()}
      onDrop={onDrop}
      className="overflow-hidden"
    >
      {/* 3.1: the poster plays instantly when available; the metadata strip
          below opens details. Unavailable items link the whole tile to
          details, same as before. */}
      {item.available ? (
        <>
          <Link to={`/watch/${item.id}`} state={{ from: backTo }} aria-label={`Play ${item.title}`} className="block">
            {thumb}
          </Link>
          <Link to={`/items/${item.id}`} className="block">
            {details}
          </Link>
        </>
      ) : (
        <Link to={`/items/${item.id}`} className="block">
          {thumb}
          {details}
        </Link>
      )}
      <div className="border-line flex items-center justify-between border-t px-2 py-2">
        <GripVertical aria-hidden className="text-tertiary size-4" strokeWidth={1.75} />
        <div className="flex gap-1">
          <IconButton aria-label="Move earlier" disabled={first} onClick={() => onMove(item.id, -1)}>
            <ArrowLeft aria-hidden className="size-4 rotate-90" strokeWidth={1.75} />
          </IconButton>
          <IconButton aria-label="Move later" disabled={last} onClick={() => onMove(item.id, 1)}>
            <ArrowLeft aria-hidden className="size-4 -rotate-90" strokeWidth={1.75} />
          </IconButton>
        </div>
      </div>
    </Card>
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

function sameNumberSet(a: number[], b: number[]): boolean {
  if (a.length !== b.length) return false
  const counts = new Map<number, number>()
  for (const value of a) counts.set(value, (counts.get(value) ?? 0) + 1)
  for (const value of b) {
    const count = counts.get(value) ?? 0
    if (count === 0) return false
    counts.set(value, count - 1)
  }
  return true
}
