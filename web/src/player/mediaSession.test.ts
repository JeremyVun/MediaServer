import { describe, expect, it } from 'vitest'
import { mediaSessionMetadata } from './mediaSession.ts'

const ORIGIN = 'http://mini.local:8484'

describe('mediaSessionMetadata', () => {
  const cases: Array<[string, string, string, string[]]> = [
    ['versioned thumbnail resolves against the origin', 'Dune', '/api/items/7/thumb?v=3', [`${ORIGIN}/api/items/7/thumb?v=3`]],
    ['an absolute thumbnail is kept', 'Dune', `${ORIGIN}/api/items/7/thumb?v=3`, [`${ORIGIN}/api/items/7/thumb?v=3`]],
    ['no thumbnail generated yet', 'Dune', '/api/items/7/thumb', []],
    ['empty thumb_url', 'Dune', '', []],
    ['another query parameter is not a version', 'Dune', '/api/items/7/thumb?size=large', []],
  ]
  it.each(cases)('%s', (_name, title, thumbURL, artwork) => {
    const metadata = mediaSessionMetadata(title, thumbURL, ORIGIN)
    expect(metadata.title).toBe(title)
    expect(metadata.artwork?.map((image) => image.src) ?? []).toEqual(artwork)
  })

  it('types the artwork as jpeg', () => {
    const metadata = mediaSessionMetadata('Dune', '/api/items/7/thumb?v=3', ORIGIN)
    expect(metadata.artwork).toEqual([{ src: `${ORIGIN}/api/items/7/thumb?v=3`, type: 'image/jpeg' }])
  })
})
