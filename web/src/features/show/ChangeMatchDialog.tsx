import { useEffect, useState } from 'react'
import { Merge, Search } from 'lucide-react'
import { ApiError } from '../../api/client.ts'
import { useMatchShow, useMergeShow, useShowMatchSearch } from '../../api/queries.ts'
import type { MatchCandidate, ShowDetail } from '../../api/types.ts'
import { mutationErrorMessage } from '../../lib/mutationFeedback.ts'
import { Button, Dialog, Input, useToast } from '../../ui/index.ts'
import { LIST, NoneThumb, OptionRow, PosterThumb } from './pickerParts.tsx'
import { defaultTvmazeSeason, fileSeasons, seasonGap, useTvmazeSeasons } from './tvmazeSeasons.ts'

const SEARCH_DEBOUNCE_MS = 300

interface Taken {
  showID: number
  name: string
}

/** Pick the show's TVmaze match, or "Not on TVmaze". The search starts from
    the show's title, which puts the weak result the classifier declined
    first. */
export function ChangeMatchDialog(props: {
  show: ShowDetail
  open: boolean
  onClose: () => void
  onMerged: (into: ShowDetail) => void
}) {
  // Remounting per opening starts the search and selection fresh.
  return <Change key={String(props.open)} {...props} />
}

function Change({
  show,
  open,
  onClose,
  onMerged,
}: {
  show: ShowDetail
  open: boolean
  onClose: () => void
  onMerged: (into: ShowDetail) => void
}) {
  const [input, setInput] = useState(show.title)
  const [query, setQuery] = useState(show.title)
  const [selectedID, setSelectedID] = useState<number | null>(show.tvmaze_id)
  const [picked, setPicked] = useState<number | null>(null)
  const [taken, setTaken] = useState<Taken | null>(null)
  const search = useShowMatchSearch(query, open)
  const seasons = useTvmazeSeasons(selectedID)
  const match = useMatchShow(show.id)
  const merge = useMergeShow(show.id)
  const { toast } = useToast()

  useEffect(() => {
    const timer = window.setTimeout(() => setQuery(input), SEARCH_DEBOUNCE_MS)
    return () => window.clearTimeout(timer)
  }, [input])

  const gap = seasons.data ? seasonGap(fileSeasons(show.seasons), seasons.data) : null
  const sameMatch = selectedID === show.tvmaze_id
  const tvSeason =
    gap == null || !seasons.data
      ? null
      : (picked ?? defaultTvmazeSeason(gap, seasons.data, sameMatch ? show.season_offset : 0))

  const select = (id: number | null) => {
    setSelectedID(id)
    setPicked(null)
    setTaken(null)
  }

  const save = () => {
    const request =
      selectedID == null
        ? { tvmaze_id: null }
        : gap != null && tvSeason != null
          ? { tvmaze_id: selectedID, season_offset: tvSeason - gap }
          : seasons.isSuccess
            ? { tvmaze_id: selectedID, season_offset: 0 }
            : { tvmaze_id: selectedID }
    match.mutate(request, {
      onSuccess: onClose,
      onError: (error) => {
        if (error instanceof ApiError && error.code === 'match_taken' && error.body?.show_id) {
          const name = search.data?.results.find((c) => c.tvmaze_id === selectedID)?.name ?? 'This TVmaze show'
          setTaken({ showID: error.body.show_id, name })
          return
        }
        toast({ message: mutationErrorMessage(error, "Couldn't change the match") })
      },
    })
  }

  const mergeInstead = (into: number) =>
    merge.mutate(into, {
      onSuccess: onMerged,
      onError: (error) => toast({ message: mutationErrorMessage(error, "Couldn't merge the shows") }),
    })

  const results = search.data?.results ?? []

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="Change match"
      width="content"
      footer={
        <>
          <Button variant="ghost" touch onClick={onClose}>
            Cancel
          </Button>
          {taken ? (
            <Button variant="primary" touch pending={merge.isPending} onClick={() => mergeInstead(taken.showID)}>
              <Merge aria-hidden className="size-5" strokeWidth={1.75} />
              Merge
            </Button>
          ) : (
            <Button variant="primary" touch pending={match.isPending} onClick={save}>
              Save
            </Button>
          )}
        </>
      }
    >
      <Input
        type="search"
        aria-label="Search TVmaze"
        placeholder="Search TVmaze"
        value={input}
        onChange={(e) => setInput(e.target.value)}
        icon={<Search className="size-4" strokeWidth={1.75} />}
      />
      <div role="radiogroup" aria-label="TVmaze show" className={LIST}>
        {results.map((candidate) => (
          <OptionRow
            key={candidate.tvmaze_id}
            thumb={<PosterThumb src={candidate.poster_url} />}
            title={candidate.name}
            meta={candidateMeta(candidate, show.id)}
            selected={candidate.tvmaze_id === selectedID}
            onSelect={() => select(candidate.tvmaze_id)}
          />
        ))}
        {search.isFetching && results.length === 0 && (
          <p className="px-2 py-3 text-sm text-secondary">Searching TVmaze…</p>
        )}
        {search.isError && <p className="px-2 py-3 text-sm text-secondary">Can't reach TVmaze right now.</p>}
        {search.isSuccess && results.length === 0 && (
          <p className="px-2 py-3 text-sm text-secondary">No shows found on TVmaze.</p>
        )}
        <OptionRow
          thumb={<NoneThumb />}
          title="Not on TVmaze"
          meta="Keep the names from the files"
          selected={selectedID == null}
          onSelect={() => select(null)}
        />
      </div>
      {gap != null && seasons.data && tvSeason != null && !taken && (
        <div className="border-line mt-4 border-t pt-4">
          <h3 className="text-sm font-medium text-secondary">Season</h3>
          <div className="mt-2 flex flex-wrap items-center gap-x-2 gap-y-2 text-base">
            <span>Files say season {gap}</span>
            <span aria-hidden className="hidden text-secondary min-[480px]:inline">
              ·
            </span>
            <label className="inline-flex items-center gap-2">
              TVmaze calls it
              <select
                value={tvSeason}
                onChange={(e) => setPicked(Number(e.target.value))}
                className="bg-inset border-line-strong text-primary h-11 rounded-sm border px-3 text-base"
              >
                {[...seasons.data].sort((a, b) => a - b).map((n) => (
                  <option key={n} value={n}>
                    Season {n}
                  </option>
                ))}
              </select>
            </label>
          </div>
        </div>
      )}
      {taken && (
        <p role="status" className="border-line mt-4 border-t pt-4 text-base">
          {taken.name} is already one of your shows. Merge {show.title} into it instead?
        </p>
      )}
    </Dialog>
  )
}

function candidateMeta(candidate: MatchCandidate, showID: number): string {
  const parts = [candidate.year ? String(candidate.year) : '', candidate.network ?? ''].filter(Boolean)
  if (candidate.local_show_id != null && candidate.local_show_id !== showID) parts.push('In your library')
  return parts.join(' · ') || 'Year unknown'
}
