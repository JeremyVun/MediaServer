import { useMemo, useState, type FormEvent, type ReactNode } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import { ArrowLeft, Clapperboard, Edit3, Layers, Play, Save, Trash2 } from 'lucide-react'
import {
  useCollections,
  useDeleteItem,
  useHealth,
  useItem,
  usePatchItem,
  useRemoveItemFromCollection,
  useRestoreItem,
} from '../../api/queries.ts'
import type { Collection, ItemDetail, MediaFile } from '../../api/types.ts'
import { formatBytes, formatClock, formatDuration } from '../../lib/format.ts'
import { makeRunOrToast, mutationErrorMessage } from '../../lib/mutationFeedback.ts'
import { withSearchParam } from '../../lib/searchParams.ts'
import { Button, Dialog, IconButton, Skeleton, useToast } from '../../ui/index.ts'

export function ItemDetailPage() {
  const id = useNumericParam()
  const item = useItem(id)
  const health = useHealth()
  const collections = useCollections()
  const deleteItem = useDeleteItem()
  const restoreItem = useRestoreItem()
  const { toast } = useToast()
  const navigate = useNavigate()
  const location = useLocation()
  // Library links here with the filtered/searched view in router state, so
  // the back link returns there instead of resetting to a bare library home.
  const cameFromApp = (location.state as { from?: string } | null)?.from != null
  const backTo = (location.state as { from?: string } | null)?.from ?? '/'
  // Pop back to the linking page's history entry (its location.key drives
  // scroll restoration there); push only for direct/external entries.
  const goBack = () => {
    if (cameFromApp) navigate(-1)
    else navigate(backTo)
  }
  const [userSelectedFileID, setUserSelectedFileID] = useState<number | null>(null)
  const [deleteOpen, setDeleteOpen] = useState(false)

  if (id == null) return <DetailShell title="Item not found" backTo={backTo} onBack={goBack} />
  if (item.isPending) return <DetailSkeleton backTo={backTo} onBack={goBack} />
  if (item.isError) return <DetailShell title="Can't load this item" backTo={backTo} onBack={goBack} />

  const defaultFile = item.data.files.find((file) => file.status === 'online') ?? item.data.files[0] ?? null
  const selectedFile =
    item.data.files.find((file) => file.id === userSelectedFileID) ?? defaultFile
  const root = health.data?.roots.find((r) => r.id === selectedFile?.root_id)
  const selectedAvailable = Boolean(selectedFile && selectedFile.status === 'online' && (root?.online ?? true))
  const watchPath = selectedFile ? `/watch/${item.data.id}?file_id=${selectedFile.id}` : `/watch/${item.data.id}`
  // "Play" is explicit start-from-zero; "Resume from…" uses the plain URL so
  // the player auto-resumes from stored progress.
  const playPath = withSearchParam(watchPath, 't', '0')
  const canResume =
    item.data.progress && !item.data.progress.completed && item.data.progress.position_s > 0
  const runOrToast = makeRunOrToast(toast)

  const onDelete = async () => {
    const deleted = item.data
    const ok = await runOrToast(() => deleteItem.mutateAsync(deleted.id), "Couldn't move to trash")
    if (!ok) return
    setDeleteOpen(false)
    navigate(backTo)
    toast({
      message: `${deleted.title} moved to trash`,
      action: {
        label: 'Undo',
        onClick: () => {
          void runOrToast(() => restoreItem.mutateAsync(deleted.id), "Couldn't restore")
        },
      },
    })
  }

  return (
    <DetailShell title={item.data.title} backTo={backTo} onBack={goBack}>
      <div className="grid gap-8 lg:grid-cols-[minmax(260px,360px)_1fr]">
        <Poster item={item.data} />
        <section className="min-w-0">
          <EditableHeader key={item.data.updated_at} item={item.data} />

          <div className="mt-5 flex flex-wrap gap-3">
            <Button
              variant="primary"
              touch
              disabled={!selectedAvailable}
              onClick={() => navigate(playPath, { state: { from: `/items/${item.data.id}` } })}
            >
              <Play aria-hidden className="size-5" strokeWidth={1.75} />
              Play
            </Button>
            {canResume && (
              <Button
                variant="secondary"
                touch
                disabled={!selectedAvailable}
                onClick={() => navigate(watchPath, { state: { from: `/items/${item.data.id}` } })}
              >
                Resume from {formatClock(item.data.progress?.position_s)}
              </Button>
            )}
          </div>

          <Facts file={selectedFile} rootName={root?.name} />

          <CollectionChips item={item.data} collections={collections.data ?? []} />

          <section className="mt-8">
            <h2 className="mb-3 text-lg font-semibold">Files</h2>
            <div className="space-y-3">
              {item.data.files.map((file) => (
                <FileRow
                  key={file.id}
                  file={file}
                  selected={file.id === selectedFile?.id}
                  onSelect={() => setUserSelectedFileID(file.id)}
                />
              ))}
            </div>
          </section>

          <section className="border-line mt-8 border-t pt-6">
            <h2 className="mb-3 text-lg font-semibold">Danger zone</h2>
            <Button variant="danger" onClick={() => setDeleteOpen(true)}>
              <Trash2 aria-hidden className="size-4" strokeWidth={1.75} />
              Move to trash
            </Button>
          </section>
        </section>
      </div>

      <Dialog
        open={deleteOpen}
        onClose={() => setDeleteOpen(false)}
        title="Move to trash?"
        footer={
          <>
            <Button variant="ghost" onClick={() => setDeleteOpen(false)}>
              Cancel
            </Button>
            <Button variant="danger" pending={deleteItem.isPending} onClick={() => void onDelete()}>
              <Trash2 aria-hidden className="size-4" strokeWidth={1.75} />
              Move to trash
            </Button>
          </>
        }
      >
        <p className="text-secondary">{item.data.title} will move to the root's trash folder.</p>
      </Dialog>
    </DetailShell>
  )
}

function DetailShell({
  title,
  backTo = '/',
  onBack,
  children,
}: {
  title: string
  backTo?: string
  // When the source page is in the history stack, back should be a history
  // pop (preserving that entry's location.key, which scroll restoration is
  // keyed on) rather than a push to a fresh entry.
  onBack?: () => void
  children?: ReactNode
}) {
  const backClass =
    'mb-6 inline-flex items-center gap-2 rounded-sm text-sm text-secondary hover:text-primary'
  return (
    <main className="mx-auto max-w-6xl px-4 py-6 sm:px-6 lg:px-8">
      {onBack ? (
        <button type="button" onClick={onBack} className={`${backClass} cursor-pointer`}>
          <ArrowLeft aria-hidden className="size-4" strokeWidth={1.75} />
          Library
        </button>
      ) : (
        <Link to={backTo} className={backClass}>
          <ArrowLeft aria-hidden className="size-4" strokeWidth={1.75} />
          Library
        </Link>
      )}
      {!children && (
        <div className="py-24 text-center">
          <p className="text-lg font-semibold">{title}</p>
        </div>
      )}
      {children}
    </main>
  )
}

function DetailSkeleton({ backTo, onBack }: { backTo: string; onBack?: () => void }) {
  return (
    <DetailShell title="Loading" backTo={backTo} onBack={onBack}>
      <div className="grid gap-8 lg:grid-cols-[minmax(260px,360px)_1fr]">
        <Skeleton className="aspect-[2/3]" />
        <div className="space-y-4">
          <Skeleton className="h-10 w-3/4" />
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-32 w-full" />
        </div>
      </div>
    </DetailShell>
  )
}

function Poster({ item }: { item: ItemDetail }) {
  const [broken, setBroken] = useState(false)
  return (
    <div className="bg-inset aspect-[2/3] overflow-hidden rounded-lg border border-line">
      {!broken ? (
        <img
          src={`/api/items/${item.id}/thumb`}
          alt=""
          onError={() => setBroken(true)}
          className="h-full w-full object-cover"
        />
      ) : (
        <div className="flex h-full w-full items-center justify-center text-tertiary">
          <Clapperboard aria-hidden className="size-14" strokeWidth={1.5} />
        </div>
      )}
    </div>
  )
}

function EditableHeader({ item }: { item: ItemDetail }) {
  const patch = usePatchItem(item.id)
  const { toast } = useToast()
  const [editing, setEditing] = useState(false)
  const [title, setTitle] = useState(item.title)
  const [year, setYear] = useState(item.year?.toString() ?? '')
  const [type, setType] = useState<ItemDetail['type']>(item.type)
  const [summary, setSummary] = useState(item.summary ?? '')

  const submit = (event: FormEvent) => {
    event.preventDefault()
    const trimmedTitle = title.trim()
    if (!trimmedTitle) return
    patch.mutate(
      {
        title: trimmedTitle,
        year: year.trim() ? Number(year) : undefined,
        type,
        summary: summary.trim(),
      },
      {
        onSuccess: () => setEditing(false),
        onError: (error) => toast({ message: mutationErrorMessage(error, "Couldn't save changes") }),
      },
    )
  }

  if (!editing) {
    return (
      <header className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <p className="mb-1 text-sm text-secondary">{[item.type, item.year].filter(Boolean).join(' · ')}</p>
          <h1 className="text-2xl font-semibold">{item.title}</h1>
          {item.summary && <p className="mt-4 max-w-3xl text-base text-secondary">{item.summary}</p>}
        </div>
        <IconButton aria-label="Edit item" onClick={() => setEditing(true)}>
          <Edit3 aria-hidden className="size-5" strokeWidth={1.75} />
        </IconButton>
      </header>
    )
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-[1fr_120px_140px]">
        <label className="space-y-1">
          <span className="text-sm text-secondary">Title</span>
          <input
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            className="bg-inset border-line-strong text-primary h-10 w-full rounded-sm border px-3 text-base"
          />
        </label>
        <label className="space-y-1">
          <span className="text-sm text-secondary">Year</span>
          <input
            value={year}
            inputMode="numeric"
            onChange={(e) => setYear(e.target.value.replace(/[^\d]/g, '').slice(0, 4))}
            className="bg-inset border-line-strong text-primary h-10 w-full rounded-sm border px-3 text-base"
          />
        </label>
        <label className="space-y-1">
          <span className="text-sm text-secondary">Type</span>
          <select
            value={type}
            onChange={(e) => setType(e.target.value as ItemDetail['type'])}
            className="bg-inset border-line-strong text-primary h-10 w-full rounded-sm border px-3 text-base"
          >
            <option value="video">Video</option>
            <option value="movie">Movie</option>
            <option value="episode">Episode</option>
          </select>
        </label>
      </div>
      <label className="block space-y-1">
        <span className="text-sm text-secondary">Summary</span>
        <textarea
          value={summary}
          onChange={(e) => setSummary(e.target.value)}
          rows={4}
          className="bg-inset border-line-strong text-primary w-full resize-y rounded-sm border px-3 py-2 text-base"
        />
      </label>
      <div className="flex gap-2">
        <Button type="submit" variant="primary" pending={patch.isPending}>
          <Save aria-hidden className="size-4" strokeWidth={1.75} />
          Save
        </Button>
        <Button variant="ghost" onClick={() => setEditing(false)}>
          Cancel
        </Button>
      </div>
    </form>
  )
}

function Facts({ file, rootName }: { file: MediaFile | null; rootName?: string }) {
  const codecs = useMemo(() => {
    if (!file) return 'Unknown codecs'
    return Array.from(new Set(file.streams.map((stream) => stream.codec))).join(', ') || 'Unknown codecs'
  }, [file])
  const resolution = file?.width && file.height ? `${file.width} x ${file.height}` : 'Unknown resolution'
  const facts: Array<{ label: string; value: string }> = [
    { label: 'Length', value: formatDuration(file?.duration_s) },
    { label: 'Resolution', value: resolution },
    { label: 'Container', value: file?.container ?? 'Unknown container' },
    { label: 'Codecs', value: codecs },
    { label: 'Size', value: formatBytes(file?.size) },
    { label: 'Root', value: rootName ?? (file ? `Root ${file.root_id}` : 'Unknown root') },
  ]
  return (
    <dl className="border-line mt-8 grid gap-4 border-y py-5 sm:grid-cols-2 lg:grid-cols-3">
      {facts.map((fact, index) => (
        <div key={`${index}-${fact.label}`}>
          <dt className="sr-only">{fact.label}</dt>
          <dd className="truncate text-sm text-secondary">{fact.value}</dd>
        </div>
      ))}
    </dl>
  )
}

function CollectionChips({ item, collections }: { item: ItemDetail; collections: Collection[] }) {
  const remove = useRemoveItemFromCollection()
  const { toast } = useToast()
  const runOrToast = makeRunOrToast(toast)
  const memberCollections = collections.filter((collection) => item.collection_ids.includes(collection.id))

  return (
    <section className="mt-8">
      <div className="mb-3 flex items-center gap-2">
        <Layers aria-hidden className="text-secondary size-4" strokeWidth={1.75} />
        <h2 className="text-lg font-semibold">Collections</h2>
      </div>
      {memberCollections.length === 0 ? (
        <p className="text-sm text-secondary">Not in a collection</p>
      ) : (
        <div className="flex flex-wrap gap-2">
          {memberCollections.map((collection) => (
            <button
              key={collection.id}
              type="button"
              onClick={() => {
                void runOrToast(
                  () => remove.mutateAsync({ collectionID: collection.id, itemID: item.id }),
                  "Couldn't update collection",
                )
              }}
              className="border-line bg-accent-fill text-accent-contrast rounded-full border px-3 py-1.5 text-sm"
            >
              {collection.name}
            </button>
          ))}
        </div>
      )}
    </section>
  )
}

function FileRow({
  file,
  selected,
  onSelect,
}: {
  file: MediaFile
  selected: boolean
  onSelect: () => void
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      className={[
        'bg-surface border-line flex w-full items-center justify-between gap-4 rounded-lg border p-4 text-left',
        selected ? 'ring-2 ring-accent' : 'hover:bg-raised',
      ].join(' ')}
    >
      <span className="min-w-0">
        <span className="block truncate text-base font-medium">{file.rel_path}</span>
        <span className="block truncate text-sm text-secondary">
          {[file.container, formatBytes(file.size), file.status].filter(Boolean).join(' · ')}
        </span>
      </span>
      <span className="shrink-0 text-sm text-secondary">{selected ? 'Selected' : 'Use'}</span>
    </button>
  )
}

function useNumericParam(): number | null {
  const params = useParams()
  const raw = params.id
  if (!raw) return null
  const id = Number(raw)
  return Number.isInteger(id) && id > 0 ? id : null
}
