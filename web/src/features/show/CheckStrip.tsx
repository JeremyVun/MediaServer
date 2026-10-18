import type { ShowDetail } from '../../api/types.ts'

const ACTION = 'text-primary hover:bg-accent-subtle h-11 cursor-pointer rounded-md px-3 text-sm font-medium min-[640px]:h-9'

/** The quiet check on a new or unsure show: an opaque strip, so it reads
    over the splash. Unmatched shows can only be changed. */
export function CheckStrip({
  show,
  confirming,
  onConfirm,
  onChange,
}: {
  show: ShowDetail
  confirming: boolean
  onConfirm: () => void
  onChange: () => void
}) {
  const matched = show.tvmaze_id != null
  return (
    <div
      data-check-strip
      className="bg-surface border-line mt-5 flex flex-col items-start rounded-md border py-1 pr-1 pl-3 text-sm min-[640px]:flex-row min-[640px]:items-center min-[640px]:gap-3"
    >
      <span className="text-secondary min-w-0 pt-2 pb-1.5 min-[640px]:py-1.5">
        {matched ? (
          <>
            Matched to{' '}
            <span className="text-primary font-medium">
              {show.title}
              {show.year ? ` (${show.year})` : ''}
            </span>{' '}
            on TVmaze
          </>
        ) : (
          'Not matched on TVmaze, so names come from the files.'
        )}
      </span>
      <span className="-ml-3 flex shrink-0 min-[640px]:ml-auto">
        {matched && (
          <button type="button" disabled={confirming} onClick={onConfirm} className={ACTION}>
            Looks right
          </button>
        )}
        <button type="button" onClick={onChange} className={ACTION}>
          Change
        </button>
      </span>
    </div>
  )
}
