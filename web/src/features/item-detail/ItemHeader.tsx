import { useState, type FormEvent } from 'react'
import { Link } from 'react-router'
import { Edit3, Save } from 'lucide-react'
import { ApiError } from '../../api/client.ts'
import { usePatchItem } from '../../api/queries.ts'
import type { ItemDetail, ItemPatch, ItemType, ShowRef } from '../../api/types.ts'
import { formatEpisodeLabel } from '../../lib/format.ts'
import { mutationErrorMessage } from '../../lib/mutationFeedback.ts'
import { Button, IconButton, useToast } from '../../ui/index.ts'
import { CheckStrip, ClampedText, MoveToShowDialog, ShowPickerDialog } from '../show/index.ts'

const FIELD = 'bg-inset border-line-strong text-primary h-10 w-full rounded-sm border px-3 text-base'
const QUIET_ACTION = 'text-primary hover:bg-accent-subtle -my-2 h-11 cursor-pointer rounded-md px-2 text-sm font-medium'

const TYPE_LABEL: Record<ItemType, string> = { movie: 'Movie', video: 'Video', episode: 'Episode' }

export function ItemHeader({
  item,
  editing,
  onEdit,
  onDone,
}: {
  item: ItemDetail
  editing: boolean
  onEdit: () => void
  onDone: () => void
}) {
  const [moveOpen, setMoveOpen] = useState(false)
  if (editing) return <EditForm key={item.updated_at} item={item} onDone={onDone} />
  return (
    <>
      <header className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <Kicker item={item} onMove={() => setMoveOpen(true)} />
          <h1 className="text-2xl font-semibold">{item.title}</h1>
          <ClampedText text={item.summary} className="mt-4" />
        </div>
        <IconButton aria-label="Edit item" onClick={onEdit}>
          <Edit3 aria-hidden className="size-5" strokeWidth={1.75} />
        </IconButton>
      </header>
      {item.needs_check && <ItemCheck item={item} onChange={item.type === 'episode' ? () => setMoveOpen(true) : onEdit} />}
      <MoveToShowDialog item={item} open={moveOpen} onClose={() => setMoveOpen(false)} />
    </>
  )
}

function Kicker({ item, onMove }: { item: ItemDetail; onMove: () => void }) {
  if (item.type !== 'episode') {
    return <p className="mb-1 text-sm text-secondary">{[TYPE_LABEL[item.type], item.year].filter(Boolean).join(' · ')}</p>
  }
  const place = placeLabel(item)
  return (
    <p className="mb-1 flex flex-wrap items-center gap-x-1 text-sm text-secondary">
      {item.show ? (
        <Link to={`/shows/${item.show.id}`} className="text-primary font-medium hover:underline">
          {item.show.title}
        </Link>
      ) : (
        <span>Episode</span>
      )}
      {place && <span>· {place}</span>}
      <button type="button" onClick={onMove} className={QUIET_ACTION}>
        Move to show
      </button>
    </p>
  )
}

/** `Season 1 · Episode 3`, `Episodes 3–4`, `Special 2`; '' when unnumbered. */
function placeLabel(item: ItemDetail): string {
  if (item.season == null || item.episode == null) return ''
  if (item.season === 0) return formatEpisodeLabel(0, item.episode)
  const episode = item.episode_end ? `Episodes ${item.episode}–${item.episode_end}` : `Episode ${item.episode}`
  return `Season ${item.season} · ${episode}`
}

function ItemCheck({ item, onChange }: { item: ItemDetail; onChange: () => void }) {
  const patch = usePatchItem(item.id)
  const { toast } = useToast()
  const confirm = () =>
    patch.mutate(
      { confirmed: true },
      { onError: (error) => toast({ message: mutationErrorMessage(error, "Couldn't save") }) },
    )

  if (item.type === 'episode' && !item.show) {
    return <CheckStrip message="This episode isn't in a show yet." onChange={onChange} changeLabel="Choose a show" />
  }
  const message =
    item.type === 'episode' && item.show ? (
      <>
        Added to <span className="text-primary font-medium">{item.show.title}</span>
        {item.season != null && item.episode != null
          ? ` as ${formatEpisodeLabel(item.season, item.episode, item.episode_end)}`
          : ''}
        .
      </>
    ) : (
      `Listed as a ${item.type}, based on its file name.`
    )
  return <CheckStrip message={message} confirming={patch.isPending} onConfirm={confirm} onChange={onChange} />
}

function EditForm({ item, onDone }: { item: ItemDetail; onDone: () => void }) {
  const patch = usePatchItem(item.id)
  const { toast } = useToast()
  const [title, setTitle] = useState(item.title)
  const [year, setYear] = useState(item.year?.toString() ?? '')
  const [type, setType] = useState<ItemType>(item.type)
  const [summary, setSummary] = useState(item.summary ?? '')
  const [show, setShow] = useState<ShowRef | null>(item.show)
  const [season, setSeason] = useState(item.season?.toString() ?? '')
  const [episode, setEpisode] = useState(item.episode?.toString() ?? '')
  const [picking, setPicking] = useState(false)
  // The type the select falls back to when the picker closes without a show.
  const [typeBeforePick, setTypeBeforePick] = useState<ItemType>(item.type)

  const chooseType = (next: ItemType) => {
    if (next === 'episode' && !show) {
      setTypeBeforePick(type)
      setPicking(true)
    }
    setType(next)
  }

  const closePicker = () => {
    setPicking(false)
    if (!show) setType(typeBeforePick)
  }

  const changes = (): ItemPatch | null => {
    const out: ItemPatch = {}
    const trimmedTitle = title.trim()
    if (trimmedTitle !== item.title) out.title = trimmedTitle
    if (year.trim() && Number(year) !== item.year) out.year = Number(year)
    if (summary.trim() !== (item.summary ?? '')) out.summary = summary.trim()
    if (type !== item.type) out.type = type
    if (type === 'episode') {
      if (!show) return null
      if (show.id !== item.show?.id) out.show_id = show.id
      if (season.trim() && Number(season) !== item.season) out.season = Number(season)
      if (episode.trim() && Number(episode) !== item.episode) {
        out.episode = Number(episode)
        if (item.episode_end != null && item.episode_end <= out.episode) out.episode_end = null
      }
    }
    return out
  }

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (!title.trim()) return
    if (type === 'episode' && !show) {
      setPicking(true)
      return
    }
    const body = changes()
    if (!body || Object.keys(body).length === 0) {
      onDone()
      return
    }
    patch.mutate(body, {
      onSuccess: onDone,
      onError: (error) => {
        if (error instanceof ApiError && error.code === 'invalid_episode') {
          toast({ message: 'Choose a show for this episode.' })
          setShow(null)
          setPicking(true)
          return
        }
        toast({ message: mutationErrorMessage(error, "Couldn't save changes") })
      },
    })
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-[1fr_120px_140px]">
        <label className="space-y-1">
          <span className="text-sm text-secondary">Title</span>
          <input value={title} onChange={(e) => setTitle(e.target.value)} className={FIELD} />
        </label>
        <label className="space-y-1">
          <span className="text-sm text-secondary">Year</span>
          <input
            value={year}
            inputMode="numeric"
            onChange={(e) => setYear(digits(e.target.value, 4))}
            className={FIELD}
          />
        </label>
        <label className="space-y-1">
          <span className="text-sm text-secondary">Type</span>
          <select value={type} onChange={(e) => chooseType(e.target.value as ItemType)} className={FIELD}>
            <option value="video">Video</option>
            <option value="movie">Movie</option>
            <option value="episode">Episode</option>
          </select>
        </label>
      </div>
      {type === 'episode' && (
        <div className="grid gap-3 sm:grid-cols-[1fr_120px_140px]">
          <div className="space-y-1">
            <span className="block text-sm text-secondary">Show</span>
            <div className="flex h-10 items-center gap-2">
              <span className="min-w-0 flex-1 truncate">{show?.title ?? 'None yet'}</span>
              <Button variant="ghost" onClick={() => setPicking(true)}>
                {show ? 'Change' : 'Choose a show'}
              </Button>
            </div>
          </div>
          <label className="space-y-1">
            <span className="text-sm text-secondary">Season</span>
            <input
              value={season}
              inputMode="numeric"
              onChange={(e) => setSeason(digits(e.target.value, 3))}
              className={FIELD}
            />
          </label>
          <label className="space-y-1">
            <span className="text-sm text-secondary">Episode</span>
            <input
              value={episode}
              inputMode="numeric"
              onChange={(e) => setEpisode(digits(e.target.value, 4).replace(/^0+/, ''))}
              className={FIELD}
            />
          </label>
        </div>
      )}
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
        <Button variant="ghost" onClick={onDone}>
          Cancel
        </Button>
      </div>
      <ShowPickerDialog
        open={picking}
        onClose={closePicker}
        title="Choose a show"
        description={`${item.title} becomes an episode of the show you pick. New files with the same name go there too.`}
        excludeShowID={show?.id}
        rankAgainst={show?.title ?? item.title}
        confirmLabel="Choose"
        onConfirm={(picked) => {
          setShow({ id: picked.id, title: picked.title })
          setPicking(false)
        }}
      />
    </form>
  )
}

function digits(value: string, max: number): string {
  return value.replace(/[^\d]/g, '').slice(0, max)
}
