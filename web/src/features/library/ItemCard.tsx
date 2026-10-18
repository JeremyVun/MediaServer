import type { ReactNode } from 'react'
import { Link } from 'react-router'
import { Clapperboard } from 'lucide-react'
import type { ItemSummary } from '../../api/types.ts'
import type { CardStyle } from '../../theme/cardStyle.ts'
import { formatDuration, progressPercent } from '../../lib/format.ts'
import { CheckBadge, OfflineBadge } from '../../ui/index.ts'
import { CardMeta, CardOverlay, ImagePlaceholder, ProgressEdge } from './cardParts.tsx'
import { SelectableCard, type CardSelection } from './SelectableCard.tsx'
import { useThumbRetry } from './useThumbRetry.ts'

/** A movie, video or not-yet-filed episode: the whole 16:9 image plus
    progress, no spine. The image plays; offline items open their page. */
export function ItemCard({
  item,
  cardStyle,
  isNew = false,
  justArrived = false,
  libraryPath,
  selection,
  resume = false,
  footer,
}: {
  item: ItemSummary
  cardStyle: CardStyle
  isNew?: boolean
  justArrived?: boolean
  libraryPath: string
  selection?: CardSelection
  /** Continue watching: the whole card resumes. */
  resume?: boolean
  /** Controls under the card, such as a collection's reorder bar. */
  footer?: ReactNode
}) {
  const progress = item.progress?.completed
    ? 100
    : progressPercent(item.progress?.position_s, item.duration_s)
  const meta = [item.year, formatDuration(item.duration_s)].filter(Boolean).join(' · ')
  const minimal = cardStyle === 'minimal'
  const thumb = <ItemThumb item={item} isNew={isNew} progress={progress} overlay={minimal} />

  return (
    <SelectableCard label={item.title} selection={selection} justArrived={justArrived}>
      {resume ? (
        <Link to={`/watch/${item.id}`} state={{ from: libraryPath }} aria-label={`Resume ${item.title}`} className="block">
          {thumb}
          {!minimal && <CardMeta title={item.title} meta={meta} />}
        </Link>
      ) : item.available ? (
        <>
          <Link to={`/watch/${item.id}`} state={{ from: libraryPath }} aria-label={`Play ${item.title}`} className="block">
            {thumb}
          </Link>
          {!minimal && (
            <Link to={`/items/${item.id}`} state={{ from: libraryPath }} className="block">
              <CardMeta title={item.title} meta={meta} />
            </Link>
          )}
        </>
      ) : (
        <Link
          to={`/items/${item.id}`}
          state={{ from: libraryPath }}
          aria-label={`Open ${item.title}, offline`}
          className="block"
        >
          {thumb}
          {!minimal && <CardMeta title={item.title} meta={meta} />}
        </Link>
      )}
      {footer}
    </SelectableCard>
  )
}

function ItemThumb({
  item,
  isNew,
  progress,
  overlay,
}: {
  item: ItemSummary
  isNew: boolean
  progress: number
  overlay: boolean
}) {
  const { broken, markBroken } = useThumbRetry(item.thumb_url)
  return (
    <div className="bg-inset relative aspect-video overflow-hidden">
      {!broken ? (
        <img src={item.thumb_url} alt="" loading="lazy" onError={markBroken} className="h-full w-full object-cover" />
      ) : (
        <ImagePlaceholder>
          <Clapperboard aria-hidden className="size-10" strokeWidth={1.5} />
        </ImagePlaceholder>
      )}
      {!item.available ? <OfflineBadge /> : item.needs_check ? <CheckBadge /> : null}
      {isNew && (
        <span className="bg-accent-fill text-on-accent absolute top-2 left-2 rounded-sm px-2 py-1 text-xs font-semibold">
          New
        </span>
      )}
      {overlay && <CardOverlay title={item.title} />}
      <ProgressEdge percent={progress} />
    </div>
  )
}
