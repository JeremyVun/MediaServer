import { useMemo, useState, type ReactNode } from 'react'
import { Search } from 'lucide-react'
import { useShowList } from '../../api/queries.ts'
import type { ShowSummary } from '../../api/types.ts'
import { Button, Dialog, Input } from '../../ui/index.ts'
import { LIST, OptionRow, PosterThumb } from './pickerParts.tsx'
import { rankShows } from './rankShows.ts'

export interface ShowPickerDialogProps {
  open: boolean
  onClose: () => void
  title: string
  /** What happens on confirm, in a sentence or two. */
  description?: ReactNode
  /** Left out of the list: the show being merged, or the episode's own show. */
  excludeShowID?: number | null
  /** Shows sharing a word with this title are listed first. */
  rankAgainst?: string
  confirmLabel: string
  confirmIcon?: ReactNode
  pending?: boolean
  onConfirm: (show: ShowSummary) => void
}

/** Pick one of your shows: the Merge dialog and item detail's Move to show
    both use it, in the Change dialog's idiom. */
export function ShowPickerDialog(props: ShowPickerDialogProps) {
  // Remounting per opening starts search and selection fresh.
  return <Picker key={String(props.open)} {...props} />
}

function Picker({
  open,
  onClose,
  title,
  description,
  excludeShowID,
  rankAgainst = '',
  confirmLabel,
  confirmIcon,
  pending,
  onConfirm,
}: ShowPickerDialogProps) {
  const shows = useShowList(open)
  const [query, setQuery] = useState('')
  const [selectedID, setSelectedID] = useState<number | null>(null)

  const list = useMemo(
    () => rankShows((shows.data ?? []).filter((s) => s.id !== excludeShowID), query, rankAgainst),
    [shows.data, excludeShowID, query, rankAgainst],
  )
  const selected = shows.data?.find((s) => s.id === selectedID) ?? null

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={title}
      width="content"
      footer={
        <>
          <Button variant="ghost" touch onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            touch
            disabled={!selected}
            pending={pending}
            onClick={() => selected && onConfirm(selected)}
          >
            {confirmIcon}
            {confirmLabel}
          </Button>
        </>
      }
    >
      {description && <p className="mb-4 text-secondary">{description}</p>}
      <Input
        type="search"
        aria-label="Search your shows"
        placeholder="Search your shows"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        icon={<Search className="size-4" strokeWidth={1.75} />}
      />
      <div role="radiogroup" aria-label="Show" className={LIST}>
        {list.map((show) => (
          <OptionRow
            key={show.id}
            thumb={<PosterThumb src={show.poster_url} />}
            title={show.title}
            meta={`${show.year ? `${show.year} · ` : ''}${show.episode_count} ${show.episode_count === 1 ? 'episode' : 'episodes'} in your library`}
            selected={show.id === selectedID}
            onSelect={() => setSelectedID(show.id)}
          />
        ))}
        {shows.isPending && <p className="px-2 py-3 text-sm text-secondary">Loading your shows…</p>}
        {shows.isError && <p className="px-2 py-3 text-sm text-secondary">Can't load your shows.</p>}
        {shows.isSuccess && list.length === 0 && (
          <p className="px-2 py-3 text-sm text-secondary">{query.trim() ? 'No show matches.' : 'No other shows yet.'}</p>
        )}
      </div>
    </Dialog>
  )
}
