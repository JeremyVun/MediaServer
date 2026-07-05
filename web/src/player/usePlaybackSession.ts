import { useMemo } from 'react'
import { usePlayItem } from '../api/queries.ts'
import type { PlayRequest } from '../api/types.ts'
import { useCapabilities } from './useCapabilities.ts'

export function usePlaybackSession(
  itemID: number,
  fileID: number | null,
  subtitleStreamIndex: number | null,
) {
  const capabilities = useCapabilities()
  const request = useMemo<PlayRequest>(
    () => ({
      file_id: fileID ?? undefined,
      capabilities,
      subtitle_stream_index: subtitleStreamIndex ?? undefined,
    }),
    [capabilities, fileID, subtitleStreamIndex],
  )
  const query = usePlayItem(itemID, request)
  return { ...query, capabilities }
}
