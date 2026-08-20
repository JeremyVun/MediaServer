import { useMemo } from 'react'
import { usePlayItem } from '../api/queries.ts'
import type { PlayRequest } from '../api/types.ts'
import { useCapabilities } from './useCapabilities.ts'

export function usePlaybackSession(
  itemID: number,
  fileID: number | null,
  subtitleStreamIndex: number | null,
  audioStreamIndex: number | null,
) {
  const capabilities = useCapabilities()
  const request = useMemo<PlayRequest>(
    () => ({
      file_id: fileID ?? undefined,
      capabilities,
      subtitle_stream_index: subtitleStreamIndex ?? undefined,
      audio_stream_index: audioStreamIndex ?? undefined,
    }),
    [audioStreamIndex, capabilities, fileID, subtitleStreamIndex],
  )
  const query = usePlayItem(itemID, request)
  return { ...query, capabilities }
}
