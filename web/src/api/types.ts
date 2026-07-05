// Mirrors SPEC-API.md response shapes (snake_case, as the server sends).

export interface HealthRoot {
  id: number
  name: string
  online: boolean
  free_bytes: number
}

export interface Health {
  version: string
  uptime_s: number
  db_ok: boolean
  roots: HealthRoot[]
  active_sessions: number
  queue_depth: number
}

export interface RootInfo {
  id: number
  name: string
  path: string
  online: boolean
  free_bytes: number
  file_count: number
}

export interface FsDir {
  name: string
  path: string
}

export interface FsDirs {
  path: string
  parent: string | null
  dirs: FsDir[]
}

export interface Progress {
  position_s: number
  completed: boolean
}

export interface ItemSummary {
  id: number
  type: 'video' | 'movie' | 'episode'
  title: string
  year: number | null
  duration_s: number | null
  thumb_url: string
  available: boolean
  created_at: string
  progress?: Progress
  collection_ids: number[]
}

export interface ItemList {
  total: number
  items: ItemSummary[]
}

export interface Collection {
  id: number
  name: string
  item_count: number
  thumb_urls: string[]
}

export interface MediaStream {
  stream_index: number
  kind: 'video' | 'audio' | 'subtitle'
  codec: string
  lang?: string
  title?: string
  channels?: number
  is_default: boolean
}

export interface MediaFile {
  id: number
  root_id: number
  rel_path: string
  size: number
  container: string | null
  duration_s: number | null
  width: number | null
  height: number | null
  bitrate: number | null
  status: 'online' | 'offline' | 'missing' | 'trashed'
  streams: MediaStream[]
}

export interface ItemDetail {
  id: number
  type: 'video' | 'movie' | 'episode'
  title: string
  year: number | null
  summary: string | null
  created_at: string
  updated_at: string
  deleted_at: string | null
  collection_ids: number[]
  progress?: Progress
  files: MediaFile[]
}

export interface ItemPatch {
  type?: ItemDetail['type']
  title?: string
  year?: number
  summary?: string
}

export interface PlayCapabilities {
  containers: string[]
  video_codecs: string[]
  audio_codecs: string[]
  max_height: number
  native_hls: boolean
}

export interface PlayRequest {
  file_id?: number
  capabilities: PlayCapabilities
  subtitle_stream_index?: number
}

export interface Subtitle {
  stream_index: number
  lang?: string
  url: string
}

export interface PlayResponse {
  mode: 'direct' | 'hls'
  reason: null | 'audio_codec' | 'video_codec' | 'container_not_supported' | 'subtitle_burn_in'
  url: string
  session_id?: string
  subtitles: Subtitle[]
}

export interface ProgressUpdate {
  position_s: number
  duration_s: number
}

export interface AddRootRequest {
  name: string
  path: string
}

export interface RescanResponse {
  job_id: number
  status: string
}

export interface Job {
  id: number
  type: string
  payload: string
  status: 'queued' | 'running' | 'done' | 'failed'
  attempts: number
  run_at: string
  started_at: string | null
  finished_at: string | null
  error: string | null
}

export interface PurgeTrashResponse {
  purged: number
  skipped: number
}

export interface CreateUploadRequest {
  filename: string
  size: number
  root_id: number
  checksum_xxh3?: string
}

export interface CreateUploadResponse {
  id: string
  chunk_size: number
}

export interface UploadStatus {
  received: number
  size: number
  status: 'active' | 'complete' | 'aborted'
  chunk_size: number
}

export interface UploadChunkResponse {
  received: number
}

export interface UploadCompleteResponse {
  item_id: number | null
}

export interface UploadProgressEvent {
  id: string
  received: number
  size: number
}

export interface UploadCompleteEvent {
  id: string
  item_id: number | null
}

export interface ApiErrorBody {
  error: {
    code: string
    message: string
  }
}
