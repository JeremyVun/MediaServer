let playerStarting = false
let queued: (() => void) | null = null

/** Runs the background route preload now, or once a starting player has
    shown its first frame, so preloading never competes with it. */
export function preloadRoutes(run: () => void) {
  if (playerStarting) queued = run
  else run()
}

export function holdPreloadForPlayer() {
  playerStarting = true
}

/** The player's first `playing`, its error, or its unmount. */
export function releasePreloadForPlayer() {
  playerStarting = false
  const run = queued
  queued = null
  run?.()
}
