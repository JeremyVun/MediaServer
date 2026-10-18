import { useState, type RefObject } from 'react'
import { ArrowLeft } from 'lucide-react'
import './splash.css'

/** Wider than this, art crops from the left: a centre crop cut the logo
    that wide TVmaze backgrounds carry on their left. */
const LEFT_CROP_ASPECT = 1.79

/** The fixed art layer the page scrolls over: the image (whole on phone,
    60vh on desktop), the back link's scrim and one full-bleed wash. */
export function SplashLayer({
  src,
  bandRef,
  onLoad,
  onError,
}: {
  src: string
  bandRef: RefObject<HTMLDivElement | null>
  onLoad: () => void
  onError: () => void
}) {
  const [wide, setWide] = useState(false)
  return (
    <div data-splash aria-hidden className="pointer-events-none fixed inset-0 overflow-hidden">
      <div ref={bandRef} className="splash-img absolute inset-x-0 top-0 min-[640px]:h-[60vh]">
        <img
          src={src}
          alt=""
          onLoad={(e) => {
            const img = e.currentTarget
            setWide(img.naturalWidth / Math.max(1, img.naturalHeight) > LEFT_CROP_ASPECT)
            onLoad()
          }}
          onError={onError}
          style={{ objectPosition: `${wide ? '0%' : '50%'} 30%` }}
          className="block h-auto w-full min-[640px]:h-full min-[640px]:object-cover"
        />
      </div>
      <div className="splash-top absolute inset-x-0 top-0 h-36" />
      <div className="splash-dim absolute inset-0" />
    </div>
  )
}

/** Reserves the art's height in the flow so the title starts below it. */
export function SplashSpacer({ src }: { src: string | null }) {
  if (!src) return <div className="h-14 min-[640px]:h-20" />
  return (
    <>
      <img src={src} alt="" aria-hidden className="invisible -mb-6 block h-auto w-full min-[640px]:hidden" />
      <div className="hidden h-[52vh] min-[640px]:block" />
    </>
  )
}

/** Back to the library: the house text link, or a dark chip over art. */
export function BackLink({ onBack, overArt }: { onBack: () => void; overArt: boolean }) {
  const icon = <ArrowLeft aria-hidden className="size-4" strokeWidth={1.75} />
  // White on black/55 holds 4.9:1 even over a white sky; bare white text
  // measured 1.39:1 over bright art.
  if (overArt) {
    return (
      <button
        type="button"
        onClick={onBack}
        className="absolute top-0 -left-1 z-10 inline-flex h-11 cursor-pointer items-center rounded-full"
      >
        <span className="inline-flex h-8 items-center gap-1.5 rounded-full bg-black/55 pr-3 pl-2 text-sm text-white">
          {icon}
          Library
        </span>
      </button>
    )
  }
  return (
    <button
      type="button"
      onClick={onBack}
      className="text-secondary hover:text-primary absolute top-0 left-0 z-10 inline-flex h-11 cursor-pointer items-center gap-2 rounded-sm text-sm"
    >
      {icon}
      Library
    </button>
  )
}
