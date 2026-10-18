import type { ReactNode } from 'react'
import { Link } from 'react-router'
import { Clapperboard, Tv } from 'lucide-react'
import type { ShowSummary } from '../../api/types.ts'
import type { CardStyle } from '../../theme/cardStyle.ts'
import { formatEpisodeLabel, progressPercent } from '../../lib/format.ts'
import { CheckBadge, OfflineBadge } from '../../ui/index.ts'
import { CardMeta, CardOverlay, ImagePlaceholder, ProgressEdge } from './cardParts.tsx'
import { SelectableCard, type CardSelection } from './SelectableCard.tsx'
import { useThumbRetry } from './useThumbRetry.ts'

/** An unstarted show with cached art is drawn as its splash and opens the
    show page; any progress (or no art) makes it a spine card. */
export function ShowCard({
  show,
  cardStyle,
  libraryPath,
  selection,
  spineOnly = false,
  footer,
}: {
  show: ShowSummary
  cardStyle: CardStyle
  libraryPath: string
  selection?: CardSelection
  /** Continue watching always shows the up-next episode. */
  spineOnly?: boolean
  /** Controls under the card, such as a collection's reorder bar. */
  footer?: ReactNode
}) {
  const backdrop = useThumbRetry(show.backdrop_url)
  const splash = !spineOnly && !show.has_progress && !backdrop.broken
  const offline = show.up_next ? !show.up_next.available : !show.available
  const badge = offline ? <OfflineBadge /> : show.needs_check ? <CheckBadge /> : null
  const minimal = cardStyle === 'minimal'
  const showPath = `/shows/${show.id}`
  const linkState = { from: libraryPath }

  if (splash) {
    const episodes = `${show.episode_count} ${show.episode_count === 1 ? 'episode' : 'episodes'}`
    return (
      <SelectableCard label={show.title} selection={selection}>
        <Link to={showPath} state={linkState} aria-label={`Open ${show.title}`} className="block">
          <div className="bg-inset relative aspect-video overflow-hidden">
            {/* Left-anchored: wider-than-16:9 art keeps its logo, which sits
                left in the art measured; 16:9 or narrower art is unaffected. */}
            <img
              src={show.backdrop_url ?? undefined}
              alt=""
              loading="lazy"
              onError={backdrop.markBroken}
              className="h-full w-full object-cover object-[0%_50%]"
            />
            {minimal && <CardOverlay title={show.title} subtitle={episodes} />}
            {badge}
          </div>
          {!minimal && <CardMeta title={show.title} meta={episodes} />}
        </Link>
        {footer}
    </SelectableCard>
    )
  }

  const up = show.up_next
  const label = up ? formatEpisodeLabel(up.season, up.episode, up.episode_end) : 'Watched'
  const playable = up?.available ?? false
  const progress = up?.progress?.completed ? 0 : progressPercent(up?.progress?.position_s, up?.duration_s)

  return (
    <SelectableCard label={show.title} selection={selection}>
      <div className="bg-inset relative flex aspect-video overflow-hidden">
        <Link
          to={showPath}
          state={linkState}
          aria-label={`Open ${show.title}`}
          className="relative block aspect-[2/3] h-full shrink-0"
        >
          <Poster url={show.poster_url} />
        </Link>
        <Link
          to={playable && up ? `/watch/${up.item_id}` : showPath}
          state={linkState}
          aria-label={`${playable ? 'Play' : 'Open'} ${show.title} ${label}`}
          className="relative block h-full min-w-0 flex-1"
        >
          <Still url={up ? up.thumb_url : show.backdrop_url} />
          {minimal && <CardOverlay title={show.title} subtitle={label} />}
          <ProgressEdge percent={progress} />
        </Link>
        {badge}
      </div>
      {!minimal && (
        <Link to={showPath} state={linkState} className="block">
          <CardMeta title={show.title} meta={label} />
        </Link>
      )}
      {footer}
    </SelectableCard>
  )
}

function Poster({ url }: { url: string | null }) {
  const { broken, markBroken } = useThumbRetry(url)
  if (broken) {
    return (
      <span className="border-line text-tertiary flex h-full w-full items-center justify-center border-r">
        <Tv aria-hidden className="size-6" strokeWidth={1.5} />
      </span>
    )
  }
  return <img src={url ?? undefined} alt="" loading="lazy" onError={markBroken} className="h-full w-full object-cover" />
}

function Still({ url }: { url: string | null }) {
  const { broken, markBroken } = useThumbRetry(url)
  if (broken) {
    return (
      <ImagePlaceholder>
        <Clapperboard aria-hidden className="size-10" strokeWidth={1.5} />
      </ImagePlaceholder>
    )
  }
  return <img src={url ?? undefined} alt="" loading="lazy" onError={markBroken} className="h-full w-full object-cover" />
}
