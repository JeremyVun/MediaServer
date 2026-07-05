import { describe, expect, it } from 'vitest'
import {
  libraryParamUpdates,
  parseLibraryParams,
  parseResumeOverride,
  withParamUpdates,
  withSearchParam,
} from './searchParams.ts'

describe('parseLibraryParams', () => {
  it('defaults when empty', () => {
    expect(parseLibraryParams(new URLSearchParams(''))).toEqual({
      q: '',
      sort: 'added',
      collectionID: undefined,
      uncollected: false,
    })
  })

  it('reads q, sort, collection', () => {
    expect(parseLibraryParams(new URLSearchParams('q=matrix&sort=title&collection=7'))).toEqual({
      q: 'matrix',
      sort: 'title',
      collectionID: 7,
      uncollected: false,
    })
  })

  it('falls back to default sort on an invalid value', () => {
    expect(parseLibraryParams(new URLSearchParams('sort=bogus')).sort).toBe('added')
  })

  it('ignores a non-numeric collection id', () => {
    expect(parseLibraryParams(new URLSearchParams('collection=abc')).collectionID).toBeUndefined()
  })

  it('reads the uncollected sentinel', () => {
    const state = parseLibraryParams(new URLSearchParams('collection=none'))
    expect(state.uncollected).toBe(true)
    expect(state.collectionID).toBeUndefined()
  })
})

describe('libraryParamUpdates', () => {
  it('omits defaults so the URL stays clean', () => {
    expect(
      libraryParamUpdates({ q: '', sort: 'added', collectionID: undefined, uncollected: false }),
    ).toEqual({
      q: undefined,
      sort: undefined,
      collection: undefined,
    })
  })

  it('includes non-default values', () => {
    expect(
      libraryParamUpdates({ q: 'matrix', sort: 'year', collectionID: 3, uncollected: false }),
    ).toEqual({
      q: 'matrix',
      sort: 'year',
      collection: '3',
    })
  })

  it('encodes the uncollected filter as the sentinel, ignoring any collectionID', () => {
    expect(
      libraryParamUpdates({ q: '', sort: 'added', collectionID: 3, uncollected: true }),
    ).toEqual({
      q: undefined,
      sort: undefined,
      collection: 'none',
    })
  })
})

describe('withParamUpdates', () => {
  it('sets and deletes keys, leaving the input untouched', () => {
    const original = new URLSearchParams('q=old&sort=title')
    const next = withParamUpdates(original, { q: 'new', sort: undefined, collection: '5' })
    expect(next.toString()).toBe('q=new&collection=5')
    expect(original.toString()).toBe('q=old&sort=title')
  })
})

describe('withSearchParam', () => {
  it('appends with ? when the path has no query string', () => {
    expect(withSearchParam('/watch/1', 't', '0')).toBe('/watch/1?t=0')
  })

  it('appends with & when the path already has a query string', () => {
    expect(withSearchParam('/watch/1?file_id=2', 't', '0')).toBe('/watch/1?file_id=2&t=0')
  })
})

describe('parseResumeOverride', () => {
  it('returns null when absent', () => {
    expect(parseResumeOverride(null)).toBeNull()
  })

  it('parses a valid non-negative number', () => {
    expect(parseResumeOverride('0')).toBe(0)
    expect(parseResumeOverride('42.5')).toBe(42.5)
  })

  it('rejects negative numbers', () => {
    expect(parseResumeOverride('-1')).toBeNull()
  })

  it('rejects non-numeric values', () => {
    expect(parseResumeOverride('abc')).toBeNull()
  })
})
