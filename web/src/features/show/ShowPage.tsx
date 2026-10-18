import { useRef, useState, type ReactNode } from 'react'
import { useLocation, useNavigate, useParams, useSearchParams } from 'react-router'
import { Ellipsis, Layers, Link2, Merge, Pencil, Play, Trash2 } from 'lucide-react'
import { ApiError } from '../../api/client.ts'
import {
  useAddShowToCollection,
  useCollections,
  useCreateCollection,
  usePatchShow,
  useRemoveShowFromCollection,
  useRestoreItem,
  useShow,
  useTrashShow,
} from '../../api/queries.ts'
import type { Collection, ShowDetail } from '../../api/types.ts'
import { formatEpisodeLabel } from '../../lib/format.ts'
import { makeRunOrToast, mutationErrorMessage } from '../../lib/mutationFeedback.ts'
import { Button, Dialog, Menu, MenuItem, MenuSeparator, OfflineLabel, Skeleton, useToast } from '../../ui/index.ts'
import { useThumbRetry } from '../library/useThumbRetry.ts'
import { CollectionListPicker } from '../library/CollectionListPicker.tsx'
import { ChangeMatchDialog } from './ChangeMatchDialog.tsx'
import { CheckStrip, ShowCheckMessage } from './CheckStrip.tsx'
import { ClampedText } from './ClampedText.tsx'
import { EpisodeTiles } from './EpisodeTiles.tsx'
import { firstPlayable, localISODate } from './episodeFormat.ts'
import { MergeShowDialog } from './MergeShowDialog.tsx'
import { RenameShowDialog } from './RenameShowDialog.tsx'
import { SeasonChips } from './SeasonChips.tsx'
import { initialSeason, seasonLabel, seasonOptions } from './seasons.ts'
import { BackLink, SplashLayer, SplashSpacer } from './Splash.tsx'
import { useSplashScroll } from './useSplashScroll.ts'

type DialogName = 'rename' | 'change' | 'merge' | 'trash'

export function ShowPage() {
  const raw = Number(useParams().id)
  const id = Number.isInteger(raw) && raw > 0 ? raw : null
  const show = useShow(id)
  const back = useBack()

  if (id == null || (show.error instanceof ApiError && show.error.status === 404)) {
    return <Shell back={back} message="Show not found" />
  }
  if (show.isError) {
    return (
      <Shell back={back} message="Can't load this show">
        <Button className="mt-4" onClick={() => void show.refetch()}>
          Try again
        </Button>
      </Shell>
    )
  }
  if (show.isPending) return <ShowSkeleton back={back} />
  return <ShowView key={show.data.id} show={show.data} back={back} />
}

interface Back {
  onBack: () => void
}

/** Back pops to the page that linked here when there is one, so its scroll
    position survives; a direct visit goes to the library. */
function useBack(): Back {
  const navigate = useNavigate()
  const location = useLocation()
  const from = (location.state as { from?: string } | null)?.from
  return { onBack: () => (from != null ? navigate(-1) : navigate('/')) }
}

function ShowView({ show, back }: { show: ShowDetail; back: Back }) {
  const pageRef = useRef<HTMLElement>(null)
  const bandRef = useRef<HTMLDivElement>(null)
  const refresh = useSplashScroll(pageRef, bandRef)
  const art = useThumbRetry(show.backdrop_url)
  const artSrc = art.broken ? null : show.backdrop_url
  const [dialog, setDialog] = useState<DialogName | null>(null)
  const [searchParams, setSearchParams] = useSearchParams()
  const navigate = useNavigate()
  const location = useLocation()
  const patch = usePatchShow(show.id)
  const { toast } = useToast()

  const options = seasonOptions(show.seasons)
  const fromURL = Number(searchParams.get('season'))
  const season = options.some((o) => o.season === fromURL) && searchParams.has('season') ? fromURL : initialSeason(show)
  const shown = show.seasons.find((s) => s.season === season)
  const tilesID = `show-${show.id}-episodes`
  const linkState = { from: `/shows/${show.id}${location.search}` }

  // The chosen season lives in the URL, so Back from the player returns to it.
  const selectSeason = (next: number) =>
    setSearchParams(
      (prev) => {
        const params = new URLSearchParams(prev)
        params.set('season', String(next))
        return params
      },
      { replace: true, state: location.state },
    )

  const close = () => setDialog(null)
  const onMerged = (into: ShowDetail) =>
    navigate(`/shows/${into.id}`, { replace: true, state: location.state })

  return (
    <main ref={pageRef} data-show-page data-art={artSrc ? '' : undefined} className="relative">
      {artSrc && <SplashLayer src={artSrc} bandRef={bandRef} onLoad={refresh} onError={art.markBroken} />}
      <div className="relative mx-auto max-w-6xl px-4 min-[640px]:px-8">
        <div className="relative pt-3 min-[640px]:pt-5">
          <BackLink onBack={back.onBack} overArt={artSrc != null} />
        </div>
      </div>
      <SplashSpacer src={artSrc} />
      <div className="relative mx-auto max-w-6xl px-4 pb-20 min-[640px]:px-8">
        <div className="min-[640px]:max-w-3xl">
          <header data-hero>
            <h1 className="text-2xl font-semibold tracking-tight">{show.title}</h1>
            <MetaLine show={show} />
            <ClampedText text={show.summary} className="mt-3" />
            {show.needs_check && (
              <CheckStrip
                message={<ShowCheckMessage show={show} />}
                confirming={patch.isPending}
                onConfirm={
                  show.tvmaze_id == null
                    ? undefined
                    : () =>
                        patch.mutate(
                          { confirmed: true },
                          { onError: (error) => toast({ message: mutationErrorMessage(error, "Couldn't save") }) },
                        )
                }
                onChange={() => setDialog('change')}
              />
            )}
            <PlayArea show={show} linkState={linkState} more={<MoreMenu show={show} onOpen={setDialog} />} />
          </header>

          <section aria-label="Episodes" className="mt-10">
            {options.length > 1 && season != null && (
              <div className="-mx-1 px-1">
                <SeasonChips
                  seasons={options}
                  selected={season}
                  onSelect={selectSeason}
                  surface="canvas"
                  controls={tilesID}
                />
              </div>
            )}
            {options.length === 1 && (
              <h2 className="flex h-11 items-center text-lg font-semibold">{seasonLabel(options[0].season, false)}</h2>
            )}
            {shown && (
              <div className="mt-3">
                <EpisodeTiles id={tilesID} season={shown} today={localISODate()} linkState={linkState} />
              </div>
            )}
            {options.length === 0 && <p className="text-secondary">No episodes in your library.</p>}
            {show.tvmaze_id != null && <p className="text-tertiary mt-8 text-xs">Show data from TVmaze</p>}
          </section>
        </div>
      </div>

      <RenameShowDialog show={show} open={dialog === 'rename'} onClose={close} />
      <ChangeMatchDialog show={show} open={dialog === 'change'} onClose={close} onMerged={onMerged} />
      <MergeShowDialog show={show} open={dialog === 'merge'} onClose={close} onMerged={onMerged} />
      <TrashShowDialog show={show} open={dialog === 'trash'} onClose={close} onTrashed={back.onBack} />
    </main>
  )
}

function MetaLine({ show }: { show: ShowDetail }) {
  const seasons = show.season_count > 0 ? `${show.season_count} ${show.season_count === 1 ? 'season' : 'seasons'}` : ''
  const meta = [show.year ? String(show.year) : '', show.network ?? '', seasons].filter(Boolean).join(' · ')
  if (!meta) return null
  return <p className="text-secondary mt-1 text-sm">{meta}</p>
}

function PlayArea({ show, linkState, more }: { show: ShowDetail; linkState: { from: string }; more: ReactNode }) {
  const navigate = useNavigate()
  const up = show.up_next
  const play = (itemID: number) => navigate(`/watch/${itemID}`, { state: linkState })
  const icon = <Play aria-hidden className="size-5" strokeWidth={1.75} />

  if (!up) {
    const first = firstPlayable(show.seasons)
    return (
      <>
        <p className="text-secondary mt-3 text-sm">You've watched every episode in your library.</p>
        <div className="mt-5 flex items-center gap-3">
          {first && (
            <Button touch className="max-[639px]:flex-1" onClick={() => play(first.id)}>
              {icon}
              Play from {formatEpisodeLabel(first.season ?? 1, first.episode ?? 1, first.episode_end)}
            </Button>
          )}
          {more}
        </div>
      </>
    )
  }

  const episode = formatEpisodeLabel(up.season, up.episode, up.episode_end)
  const resume = up.progress != null && !up.progress.completed && up.progress.position_s > 0
  const label = `${resume ? 'Resume' : 'Play'} ${episode}`
  if (!up.available) {
    return (
      <div className="mt-5 flex items-center gap-3">
        <Button variant="primary" touch disabled>
          {icon}
          {label}
        </Button>
        <OfflineLabel className="text-primary text-sm" />
        <span className="grow" />
        {more}
      </div>
    )
  }
  return (
    <div className="mt-5 flex gap-3">
      <Button variant="primary" touch className="max-[639px]:flex-1" onClick={() => play(up.item_id)}>
        {icon}
        {label}
      </Button>
      {more}
    </div>
  )
}

function MoreMenu({ show, onOpen }: { show: ShowDetail; onOpen: (dialog: DialogName) => void }) {
  const collections = useCollections()
  const add = useAddShowToCollection()
  const remove = useRemoveShowFromCollection()
  const create = useCreateCollection()
  const { toast } = useToast()
  const runOrToast = makeRunOrToast(toast)
  const [filter, setFilter] = useState('')
  const iconClass = 'size-4'

  const toggle = (collection: Collection) =>
    void runOrToast(
      () =>
        show.collection_ids.includes(collection.id)
          ? remove.mutateAsync({ collectionID: collection.id, showID: show.id })
          : add.mutateAsync({ collectionID: collection.id, showID: show.id }),
      "Couldn't update collection",
    )

  return (
    <Menu
      aria-label="More"
      trigger={<Ellipsis aria-hidden className="size-5" strokeWidth={1.75} />}
      triggerClassName="border-line text-primary hover:bg-accent-subtle inline-flex size-11 shrink-0 cursor-pointer items-center justify-center rounded-md border"
      onOpenChange={(open) => !open && setFilter('')}
    >
      {({ view, setView }) =>
        view === 'collections' ? (
          <CollectionListPicker
            collections={collections.data ?? []}
            filter={filter}
            setFilter={setFilter}
            isChecked={(c) => show.collection_ids.includes(c.id)}
            keepOpenOnPick
            onPick={toggle}
            onCreate={(name) =>
              void runOrToast(async () => {
                const collection = await create.mutateAsync(name)
                await add.mutateAsync({ collectionID: collection.id, showID: show.id })
              }, "Couldn't create collection")
            }
          />
        ) : (
          <>
            <MenuItem icon={<Pencil className={iconClass} strokeWidth={1.75} />} onSelect={() => onOpen('rename')}>
              Rename
            </MenuItem>
            <MenuItem icon={<Link2 className={iconClass} strokeWidth={1.75} />} onSelect={() => onOpen('change')}>
              Change match
            </MenuItem>
            <MenuItem icon={<Merge className={iconClass} strokeWidth={1.75} />} onSelect={() => onOpen('merge')}>
              Merge into another show
            </MenuItem>
            <MenuItem
              icon={<Layers className={iconClass} strokeWidth={1.75} />}
              closeOnSelect={false}
              onSelect={() => setView('collections')}
            >
              Add to collection…
            </MenuItem>
            <MenuSeparator />
            <MenuItem danger icon={<Trash2 className={iconClass} strokeWidth={1.75} />} onSelect={() => onOpen('trash')}>
              Move to trash
            </MenuItem>
          </>
        )
      }
    </Menu>
  )
}

function TrashShowDialog({
  show,
  open,
  onClose,
  onTrashed,
}: {
  show: ShowDetail
  open: boolean
  onClose: () => void
  onTrashed: () => void
}) {
  const trash = useTrashShow()
  const restore = useRestoreItem()
  const { toast } = useToast()
  const runOrToast = makeRunOrToast(toast)
  const episodes = `${show.episode_count} ${show.episode_count === 1 ? 'episode' : 'episodes'}`

  const onTrash = async () => {
    let restoreIDs: number[] = []
    const ok = await runOrToast(async () => {
      restoreIDs = (await trash.mutateAsync(show.id)).item_ids
    }, "Couldn't move to trash")
    if (!ok) return
    onClose()
    onTrashed()
    toast({
      message: `${show.title} moved to trash`,
      action: {
        label: 'Undo',
        onClick: () =>
          void runOrToast(async () => {
            for (const id of restoreIDs) await restore.mutateAsync(id)
          }, "Couldn't restore"),
      },
    })
  }

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="Move to trash?"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="danger" pending={trash.isPending} onClick={() => void onTrash()}>
            <Trash2 aria-hidden className="size-4" strokeWidth={1.75} />
            Move to trash
          </Button>
        </>
      }
    >
      <p className="text-secondary">
        {show.title} and its {episodes} will move to the root's trash folder.
      </p>
    </Dialog>
  )
}

function Shell({ back, message, children }: { back: Back; message: string; children?: ReactNode }) {
  return (
    <main className="mx-auto max-w-6xl px-4 pt-3 min-[640px]:px-8 min-[640px]:pt-5">
      <div className="relative h-11">
        <BackLink onBack={back.onBack} overArt={false} />
      </div>
      <div className="flex flex-col items-center py-24 text-center">
        <p className="text-lg font-semibold">{message}</p>
        {children}
      </div>
    </main>
  )
}

function ShowSkeleton({ back }: { back: Back }) {
  return (
    <main aria-busy="true" className="mx-auto max-w-6xl px-4 pt-3 pb-20 min-[640px]:px-8 min-[640px]:pt-5">
      <div className="relative h-11">
        <BackLink onBack={back.onBack} overArt={false} />
      </div>
      <div className="mt-16 space-y-3 min-[640px]:max-w-3xl">
        <Skeleton className="h-10 w-2/3" />
        <Skeleton className="h-4 w-1/3" />
        <Skeleton className="h-16 w-full" />
        <Skeleton className="mt-5 h-11 w-48" />
        <div className="grid grid-cols-2 gap-4 pt-10 min-[640px]:grid-cols-3">
          {Array.from({ length: 6 }, (_, i) => (
            <Skeleton key={i} className="aspect-video" />
          ))}
        </div>
      </div>
    </main>
  )
}
