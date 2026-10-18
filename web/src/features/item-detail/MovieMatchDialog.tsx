import { useEffect, useState } from 'react'
import { Clapperboard, Search } from 'lucide-react'
import { ApiError } from '../../api/client.ts'
import { useMatchMovie, useMovieMatchSearch } from '../../api/queries.ts'
import type { ItemDetail } from '../../api/types.ts'
import { mutationErrorMessage } from '../../lib/mutationFeedback.ts'
import { Button, Dialog, Input, useToast } from '../../ui/index.ts'
import { LIST, NoneThumb, OptionRow, PosterThumb } from '../show/index.ts'

const SEARCH_DEBOUNCE_MS = 300

/** Pick a movie's TMDB match, or "Not on TMDB". The search starts from the
    item's title and year, which puts the weak result the classifier
    declined first. */
export function MovieMatchDialog(props: { item: ItemDetail; open: boolean; onClose: () => void }) {
  // Remounting per opening starts the search and selection fresh.
  return <Change key={String(props.open)} {...props} />
}

function Change({ item, open, onClose }: { item: ItemDetail; open: boolean; onClose: () => void }) {
  const [input, setInput] = useState(item.title)
  const [query, setQuery] = useState(item.title)
  const [selectedID, setSelectedID] = useState<number | null>(item.tmdb_id)
  // The year belongs to the file's title; a search the owner typed goes without it.
  const search = useMovieMatchSearch(item.id, query, query === item.title ? item.year : null, open)
  const match = useMatchMovie(item.id)
  const { toast } = useToast()

  useEffect(() => {
    const timer = window.setTimeout(() => setQuery(input), SEARCH_DEBOUNCE_MS)
    return () => window.clearTimeout(timer)
  }, [input])

  const save = () =>
    match.mutate(
      { tmdb_id: selectedID },
      {
        onSuccess: onClose,
        onError: (error) =>
          toast({
            message:
              error instanceof ApiError && error.code === 'tmdb_unavailable'
                ? "Can't reach TMDB right now. Try again later."
                : mutationErrorMessage(error, "Couldn't change the match"),
          }),
      },
    )

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
          <Button variant="primary" touch pending={match.isPending} onClick={save}>
            Save
          </Button>
        </>
      }
    >
      <Input
        type="search"
        aria-label="Search TMDB"
        placeholder="Search TMDB"
        value={input}
        onChange={(e) => setInput(e.target.value)}
        icon={<Search className="size-4" strokeWidth={1.75} />}
      />
      <div role="radiogroup" aria-label="TMDB movie">
        <div className={LIST}>
          {results.map((candidate) => (
            <OptionRow
              key={candidate.tmdb_id}
              thumb={<PosterThumb src={candidate.poster_url} Glyph={Clapperboard} />}
              title={candidate.title}
              meta={candidate.year ? String(candidate.year) : 'Year unknown'}
              selected={candidate.tmdb_id === selectedID}
              onSelect={() => setSelectedID(candidate.tmdb_id)}
            />
          ))}
          {search.isFetching && results.length === 0 && (
            <p className="px-2 py-3 text-sm text-secondary">Searching TMDB…</p>
          )}
          {search.isError && !search.isFetching && (
            <p className="px-2 py-3 text-sm text-secondary">Can't reach TMDB right now.</p>
          )}
          {search.isSuccess && results.length === 0 && (
            <p className="px-2 py-3 text-sm text-secondary">No movies found on TMDB.</p>
          )}
        </div>
        {/* Outside the scroll area, so the current state stays in view on open. */}
        <div className="-mx-2 mt-1 px-2">
          <OptionRow
            thumb={<NoneThumb />}
            title="Not on TMDB"
            meta="Keep the title from the file"
            selected={selectedID == null}
            onSelect={() => setSelectedID(null)}
          />
        </div>
      </div>
    </Dialog>
  )
}
