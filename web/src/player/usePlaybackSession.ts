import { useMemo } from 'react'
import { usePlayItem } from '../api/queries.ts'
import type { PlayRequest, Quality } from '../api/types.ts'
import { useCapabilities } from './useCapabilities.ts'

export function usePlaybackSession(
  itemID: number,
  fileID: number | null,
  subtitleStreamIndex: number | null,
  audioStreamIndex: number | null,
  quality: Quality,
) {
  const capabilities = useCapabilities()
  const request = useMemo<PlayRequest>(
    () => ({
      file_id: fileID ?? undefined,
      capabilities,
      subtitle_stream_index: subtitleStreamIndex ?? undefined,
      audio_stream_index: audioStreamIndex ?? undefined,
      quality,
    }),
    [audioStreamIndex, capabilities, fileID, quality, subtitleStreamIndex],
  )
  const query = usePlayItem(itemID, request)
  return { ...query, capabilities }
}
