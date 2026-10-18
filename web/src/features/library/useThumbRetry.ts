import { useCallback, useState } from 'react'

// Image URLs are versioned (?v= appears when the image lands, changes when it
// regenerates), so a new URL means an earlier load failure no longer applies:
// re-seed during render to remount the <img> and retry.
export function useThumbRetry(url: string | null) {
  const [broken, setBroken] = useState(false)
  const [syncedUrl, setSyncedUrl] = useState(url)
  if (url !== syncedUrl) {
    setSyncedUrl(url)
    setBroken(false)
  }
  const markBroken = useCallback(() => setBroken(true), [])
  return { broken: broken || url == null, markBroken }
}
