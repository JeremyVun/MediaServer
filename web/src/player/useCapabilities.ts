import { useMemo } from 'react'
import type { PlayCapabilities } from '../api/types.ts'

export function useCapabilities(): PlayCapabilities {
  return useMemo(() => {
    if (typeof document === 'undefined') {
      return {
        containers: ['mp4'],
        video_codecs: ['h264'],
        audio_codecs: ['aac', 'mp3'],
        max_height: 1080,
        native_hls: false,
      }
    }

    const video = document.createElement('video')
    const containers = ['mp4']
    if (video.canPlayType('video/quicktime')) containers.push('mov')

    const videoCodecs = ['h264']
    if (video.canPlayType('video/mp4; codecs="hvc1.1.6.L123.B0"') !== '') {
      videoCodecs.push('hevc')
    }

    const audioCodecs = ['aac', 'mp3']
    if (video.canPlayType('audio/mp4; codecs="ac-3"') !== '') audioCodecs.push('ac3')
    if (video.canPlayType('audio/mp4; codecs="ec-3"') !== '') audioCodecs.push('eac3')

    return {
      containers,
      video_codecs: videoCodecs,
      audio_codecs: audioCodecs,
      max_height: Math.min(2160, Math.ceil(screen.height * window.devicePixelRatio)),
      native_hls: video.canPlayType('application/vnd.apple.mpegurl') !== '',
    }
  }, [])
}
