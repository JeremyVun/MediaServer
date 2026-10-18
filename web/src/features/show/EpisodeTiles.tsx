import { Link } from 'react-router'
import { Clapperboard, HardDrive } from 'lucide-react'
import type { MissingEpisode, ShowSeason } from '../../api/types.ts'
import { ImagePlaceholder, ProgressEdge } from '../library/cardParts.tsx'
import { useThumbRetry } from '../library/useThumbRetry.ts'
import {
  episodeNumber,
  episodeProgress,
  episodeState,
  foldSummary,
  isWhollyMissing,
  missingLine,
  type ItemEpisode,
} from './episodeFormat.ts'

const TILE_GRID =
  'grid grid-cols-[repeat(auto-fill,minmax(160px,1fr))] gap-4 min-[640px]:grid-cols-[repeat(auto-fill,minmax(240px,1fr))]'

const FOOT =
  'pointer-events-none absolute inset-x-0 bottom-0 bg-gradient-to-t from-black/85 via-black/30 to-transparent px-2.5 pt-10 pb-2.5'

/** Imageless tiles carry the canvas as their own fill, so the dashed border
    holds 3:1 wherever the splash sits behind them. */
const DASHED = 'bg-canvas border-tertiary relative aspect-video rounded-lg border border-dashed'

/** One season's episodes as image tiles; a season with nothing in the
    library folds into a single tile. */
export function EpisodeTiles({
  id,
  season,
  today,
  linkState,
}: {
  id?: string
  season: ShowSeason
  today: string
  linkState: { from: string }
}) {
  const folded = isWhollyMissing(season) && season.episodes.length > 0
  return (
    <div id={id} role="tabpanel" data-season={season.season} data-tiles className={TILE_GRID}>
      {folded ? (
        <FoldTile episodes={season.episodes as MissingEpisode[]} today={today} />
      ) : (
        season.episodes.map((entry) =>
          entry.kind === 'missing' ? (
            <MissingTile key={`m${entry.number}`} episode={entry} today={today} />
          ) : (
            <EpisodeTile key={entry.id} item={entry} linkState={linkState} />
          ),
        )
      )}
    </div>
  )
}

function EpisodeTile({ item, linkState }: { item: ItemEpisode; linkState: { from: string } }) {
  const thumb = useThumbRetry(item.thumb_url)
  const number = episodeNumber(item.season ?? 1, item.episode, item.episode_end)
  const state = episodeState(item)
  const label = `${number ? `${number} ` : ''}${item.title}${item.available ? '' : ', offline'}`
  return (
    <Link
      to={item.available ? `/watch/${item.id}` : `/items/${item.id}`}
      state={linkState}
      aria-label={label}
      data-state={item.available ? 'available' : 'offline'}
      className="bg-inset border-line relative block aspect-video overflow-hidden rounded-lg border"
    >
      {thumb.broken ? (
        <ImagePlaceholder>
          <Clapperboard aria-hidden className="size-8" strokeWidth={1.5} />
        </ImagePlaceholder>
      ) : (
        <img
          src={item.thumb_url}
          alt=""
          loading="lazy"
          onError={thumb.markBroken}
          className="h-full w-full object-cover"
        />
      )}
      <span className={FOOT}>
        <span className="flex gap-1.5 text-sm font-medium text-white">
          {number && <span className="tabular text-white/75">{number}</span>}
          <span className="truncate">{item.title}</span>
        </span>
        <span className="mt-0.5 flex min-w-0 items-center gap-1 text-xs text-white/75">
          {!item.available && (
            <>
              <span className="inline-flex shrink-0 items-center gap-1 font-medium text-white">
                <HardDrive aria-hidden className="size-3.5 shrink-0" strokeWidth={1.75} />
                Offline
              </span>
              <span aria-hidden>·</span>
            </>
          )}
          {state.map((part, i) => (
            <span key={part} className={i === state.length - 1 ? 'flex min-w-0 items-center gap-1' : 'flex shrink-0 items-center gap-1'}>
              {i > 0 && <span aria-hidden>·</span>}
              <span className={i === state.length - 1 ? 'truncate' : ''}>{part}</span>
            </span>
          ))}
        </span>
      </span>
      <ProgressEdge percent={episodeProgress(item)} />
    </Link>
  )
}

function MissingTile({ episode, today }: { episode: MissingEpisode; today: string }) {
  const line = missingLine(episode, today)
  return (
    <div aria-disabled="true" data-state={line.lead ? 'missing' : 'upcoming'} className={DASHED}>
      <span className="absolute inset-x-0 bottom-0 px-2.5 pb-2.5">
        <span className="text-primary flex gap-1.5 text-sm font-medium">
          {episode.season !== 0 && <span className="tabular text-secondary">{episode.number}</span>}
          <span className="truncate">{episode.title}</span>
        </span>
        <span className="mt-0.5 block truncate text-xs">
          {line.lead && <span className="text-primary font-medium">{line.lead}</span>}
          <span className="text-secondary">{line.rest}</span>
        </span>
      </span>
    </div>
  )
}

function FoldTile({ episodes, today }: { episodes: MissingEpisode[]; today: string }) {
  const { title, meta } = foldSummary(episodes, today)
  return (
    <div aria-disabled="true" data-state="folded" className={DASHED}>
      <span className="absolute inset-x-0 bottom-0 px-2.5 pb-2.5">
        <span className="text-primary block truncate text-sm font-medium">{title}</span>
        {meta && <span className="text-secondary mt-0.5 block truncate text-xs">{meta}</span>}
      </span>
    </div>
  )
}
