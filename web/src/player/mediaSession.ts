/**
 * Lock-screen metadata for the item playing. Artwork is left out until the
 * thumbnail exists: the URL only carries a `?v=` once one has been generated.
 * Kept free of React and of the Media Session API so it can be table-tested.
 */
export function mediaSessionMetadata(title: string, thumbURL: string, origin: string): MediaMetadataInit {
  if (!thumbURL.includes('?v=')) return { title, artwork: [] }
  return { title, artwork: [{ src: new URL(thumbURL, origin).toString(), type: 'image/jpeg' }] }
}
