/** A thumbnail URL only carries a `?v=` once the thumbnail has been generated. */
export function thumbnailGenerated(thumbURL: string | undefined): boolean {
  return thumbURL != null && thumbURL.includes('?v=')
}

/**
 * Lock-screen metadata for the item playing; an episode passes its show as
 * the artist. Artwork is left out until the thumbnail exists.
 * Kept free of React and of the Media Session API so it can be table-tested.
 */
export function mediaSessionMetadata(
  title: string,
  thumbURL: string,
  origin: string,
  artist?: string,
): MediaMetadataInit {
  const named = artist ? { title, artist } : { title }
  if (!thumbnailGenerated(thumbURL)) return { ...named, artwork: [] }
  return { ...named, artwork: [{ src: new URL(thumbURL, origin).toString(), type: 'image/jpeg' }] }
}
