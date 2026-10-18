import { useMemo, useRef, useState, type ReactNode } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import { ArrowLeft, Clapperboard, Ellipsis, Layers, Link2, Pencil, Play, RotateCcw, Trash2 } from 'lucide-react'
import {
  useCollections,
  useDeleteItem,
  useHealth,
  useItem,
  useRemoveItemFromCollection,
  useRestoreItem,
  useSettings,
} from '../../api/queries.ts'
import type { Collection, ItemDetail, MediaFile } from '../../api/types.ts'
import { formatBytes, formatClock, formatDuration } from '../../lib/format.ts'
import { makeRunOrToast } from '../../lib/mutationFeedback.ts'
import { withSearchParam } from '../../lib/searchParams.ts'
import { Button, Dialog, Menu, MenuItem, MenuSeparator, OfflineLabel, Skeleton, useToast } from '../../ui/index.ts'
import { useThumbRetry } from '../library/useThumbRetry.ts'
import { BackLink, ClampedText, SplashLayer, SplashSpacer, useSplashScroll } from '../show/index.ts'
import { EditForm, ItemCheck, ItemHeader } from './ItemHeader.tsx'
import { MovieMatchDialog } from './MovieMatchDialog.tsx'

const TMDB_NOTICE = 'This product uses the TMDB API but is not endorsed or certified by TMDB.'

export function ItemDetailPage() {
  const id = useNumericParam()
  const item = useItem(id)
  const health = useHealth()
  const settings = useSettings()
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
  const [editing, setEditing] = useState(false)
  const [matchOpen, setMatchOpen] = useState(false)
  const art = useThumbRetry(item.data?.art_url ?? null)

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

  const onChangeMatch =
    settings.data?.tmdb?.configured && item.data.type === 'movie' ? () => setMatchOpen(true) : undefined
  const fromHere = { state: { from: `/items/${item.data.id}` } }
  const files = (
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
  )
  const soleFile = item.data.files.length === 1 ? item.data.files[0] : null
  const notice = item.data.tmdb_id != null && <p className="text-tertiary mt-10 text-xs">{TMDB_NOTICE}</p>
  const dialogs = (
    <>
      {onChangeMatch && <MovieMatchDialog item={item.data} open={matchOpen} onClose={() => setMatchOpen(false)} />}
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
    </>
  )

  if (item.data.art_url && !art.broken) {
    const play = () => navigate(canResume ? watchPath : playPath, fromHere)
    return (
      <MovieSplash art={item.data.art_url} onArtError={art.markBroken} onBack={goBack}>
        <header data-hero>
          {editing ? (
            <EditForm key={item.data.updated_at} item={item.data} onDone={() => setEditing(false)} />
          ) : (
            <>
              <h1 className="text-2xl font-semibold tracking-tight">{item.data.title}</h1>
              <MovieMeta year={item.data.year} durationS={selectedFile?.duration_s} />
              <ClampedText text={item.data.summary} className="mt-3" />
              {item.data.needs_check && (
                <ItemCheck item={item.data} tmdb={onChangeMatch != null} onChange={onChangeMatch ?? (() => setEditing(true))} />
              )}
              <div className="mt-5 flex items-center gap-3">
                <Button
                  variant="primary"
                  touch
                  disabled={!selectedAvailable}
                  className={selectedAvailable ? 'max-[639px]:flex-1' : ''}
                  onClick={play}
                >
                  <Play aria-hidden className="size-5" strokeWidth={1.75} />
                  {canResume ? `Resume from ${formatClock(item.data.progress?.position_s)}` : 'Play'}
                </Button>
                {selectedFile && !selectedAvailable && (
                  <>
                    <OfflineLabel className="text-primary text-sm" />
                    <span className="grow" />
                  </>
                )}
                <MovieMenu
                  onEdit={() => setEditing(true)}
                  onChangeMatch={onChangeMatch}
                  onPlayFromStart={canResume && selectedAvailable ? () => navigate(playPath, fromHere) : undefined}
                  onTrash={() => setDeleteOpen(true)}
                />
              </div>
            </>
          )}
        </header>
        <CollectionChips item={item.data} collections={collections.data ?? []} className="mt-10" />
        {soleFile ? (
          <section className="mt-8">
            <h2 className="mb-3 text-lg font-semibold">Files</h2>
            <SoleFile file={soleFile} />
          </section>
        ) : (
          files
        )}
        {notice}
        {dialogs}
      </MovieSplash>
    )
  }

  return (
    <DetailShell title={item.data.title} backTo={backTo} onBack={goBack}>
      <div className="grid gap-8 lg:grid-cols-[minmax(260px,360px)_1fr]">
        <Poster item={item.data} />
        <section className="min-w-0">
          <ItemHeader
            item={item.data}
            editing={editing}
            onEdit={() => setEditing(true)}
            onDone={() => setEditing(false)}
            onChangeMatch={onChangeMatch}
          />

          <div className="mt-5 flex flex-wrap items-center gap-3">
            <Button variant="primary" touch disabled={!selectedAvailable} onClick={() => navigate(playPath, fromHere)}>
              <Play aria-hidden className="size-5" strokeWidth={1.75} />
              Play
            </Button>
            {canResume && (
              <Button variant="secondary" touch disabled={!selectedAvailable} onClick={() => navigate(watchPath, fromHere)}>
                Resume from {formatClock(item.data.progress?.position_s)}
              </Button>
            )}
            {selectedFile && !selectedAvailable && <OfflineLabel className="text-primary text-sm" />}
          </div>

          <Facts file={selectedFile} rootName={root?.name} />

          <CollectionChips item={item.data} collections={collections.data ?? []} />

          {files}

          <section className="border-line mt-8 border-t pt-6">
            <h2 className="mb-3 text-lg font-semibold">Danger zone</h2>
            <Button variant="danger" onClick={() => setDeleteOpen(true)}>
              <Trash2 aria-hidden className="size-4" strokeWidth={1.75} />
              Move to trash
            </Button>
          </section>
          {notice}
        </section>
      </div>
      {dialogs}
    </DetailShell>
  )
}

/** A movie with a cached TMDB backdrop: the show page's splash header. */
function MovieSplash({
  art,
  onArtError,
  onBack,
  children,
}: {
  art: string
  onArtError: () => void
  onBack: () => void
  children: ReactNode
}) {
  const pageRef = useRef<HTMLElement>(null)
  const bandRef = useRef<HTMLDivElement>(null)
  const refresh = useSplashScroll(pageRef, bandRef)
  return (
    <main ref={pageRef} data-show-page data-art="" className="relative">
      <SplashLayer src={art} bandRef={bandRef} onLoad={refresh} onError={onArtError} />
      <div className="relative mx-auto max-w-6xl px-4 min-[640px]:px-8">
        <div className="relative pt-3 min-[640px]:pt-5">
          <BackLink onBack={onBack} overArt />
        </div>
      </div>
      <SplashSpacer src={art} />
      <div className="relative mx-auto max-w-6xl px-4 pb-20 min-[640px]:px-8">
        <div className="min-[640px]:max-w-3xl">{children}</div>
      </div>
    </main>
  )
}

function MovieMeta({ year, durationS }: { year: number | null; durationS: number | null | undefined }) {
  const meta = [year ? String(year) : '', durationS ? formatDuration(durationS) : ''].filter(Boolean).join(' · ')
  if (!meta) return null
  return <p className="text-secondary mt-1 text-sm">{meta}</p>
}

function MovieMenu({
  onEdit,
  onChangeMatch,
  onPlayFromStart,
  onTrash,
}: {
  onEdit: () => void
  onChangeMatch?: () => void
  onPlayFromStart?: () => void
  onTrash: () => void
}) {
  const iconClass = 'size-4'
  return (
    <Menu
      aria-label="More"
      trigger={<Ellipsis aria-hidden className="size-5" strokeWidth={1.75} />}
      triggerClassName="border-line text-primary hover:bg-accent-subtle inline-flex size-11 shrink-0 cursor-pointer items-center justify-center rounded-md border"
    >
      {onPlayFromStart && (
        <MenuItem icon={<RotateCcw className={iconClass} strokeWidth={1.75} />} onSelect={onPlayFromStart}>
          Play from start
        </MenuItem>
      )}
      <MenuItem icon={<Pencil className={iconClass} strokeWidth={1.75} />} onSelect={onEdit}>
        Edit details
      </MenuItem>
      {onChangeMatch && (
        <MenuItem icon={<Link2 className={iconClass} strokeWidth={1.75} />} onSelect={onChangeMatch}>
          Change match
        </MenuItem>
      )}
      <MenuSeparator />
      <MenuItem danger icon={<Trash2 className={iconClass} strokeWidth={1.75} />} onSelect={onTrash}>
        Move to trash
      </MenuItem>
    </Menu>
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
        <Skeleton className="aspect-video lg:aspect-[2/3]" />
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
    <div className="bg-inset aspect-video overflow-hidden rounded-lg border border-line lg:aspect-[2/3]">
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

function CollectionChips({
  item,
  collections,
  className = 'mt-8',
}: {
  item: ItemDetail
  collections: Collection[]
  className?: string
}) {
  const remove = useRemoveItemFromCollection()
  const { toast } = useToast()
  const runOrToast = makeRunOrToast(toast)
  const memberCollections = collections.filter((collection) => item.collection_ids.includes(collection.id))

  return (
    <section className={className}>
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
              className="border-line-strong text-primary hover:bg-raised rounded-full border px-3 py-1.5 text-sm"
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
        selected ? 'border-primary' : 'hover:bg-raised',
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

/** With one file there is nothing to pick: the row only names it. */
function SoleFile({ file }: { file: MediaFile }) {
  return (
    <div data-file-row className="bg-surface border-line rounded-lg border p-4">
      <span className="block truncate text-base font-medium">{file.rel_path}</span>
      <span className="block truncate text-sm text-secondary">
        {[file.duration_s ? formatDuration(file.duration_s) : '', file.status].filter(Boolean).join(' · ')}
      </span>
    </div>
  )
}

function useNumericParam(): number | null {
  const params = useParams()
  const raw = params.id
  if (!raw) return null
  const id = Number(raw)
  return Number.isInteger(id) && id > 0 ? id : null
}
