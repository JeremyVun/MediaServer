import { thumbnailGenerated } from './mediaSession.ts'

/**
 * The still shown while Audio only is the resolved quality: the session has no
 * video track, so the item's thumbnail stands in for the picture. Nothing is
 * shown until a thumbnail exists — the bare URL answers 416.
 */
export function AudioOnlyBackdrop({ thumbURL }: { thumbURL?: string }) {
  if (!thumbnailGenerated(thumbURL)) return null
  return (
    // Black, not canvas, so the cover meets the video's own letterbox seamlessly;
    // inert so taps and hover still reach the video area's gesture handling.
    <div className="pointer-events-none absolute inset-0 flex items-center justify-center bg-black p-6 md:p-12">
      <img src={thumbURL} alt="" className="max-h-full max-w-full rounded-lg object-contain" />
    </div>
  )
}
