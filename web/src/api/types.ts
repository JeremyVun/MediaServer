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

export interface HLSCacheSettings {
  dir: string
  max_bytes: number
  used_bytes: number
  available: boolean
}

export interface SemanticClassificationSettings {
  enabled: boolean
  available: boolean
}

export interface SettingsResponse {
  hls_cache: HLSCacheSettings
  semantic_classification: SemanticClassificationSettings
  /** Whether the server has a TMDB token; the token itself never leaves it. */
  tmdb: { configured: boolean }
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

export type ItemType = 'video' | 'movie' | 'episode'

export interface ShowRef {
  id: number
  title: string
}

/** An item's place in a show; null/false for movies, videos and pending episodes. */
export interface EpisodeFields {
  show: ShowRef | null
  season: number | null
  episode: number | null
  episode_end: number | null
  needs_check: boolean
}

/** TMDB movie data; null unless the server has a token and matched a movie. */
export interface MovieFields {
  tmdb_id: number | null
  /** The cached TMDB backdrop, null until cached. */
  art_url: string | null
}

export interface ItemSummary extends EpisodeFields, MovieFields {
  id: number
  type: ItemType
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

export interface UpNext {
  item_id: number
  season: number
  episode: number
  episode_end: number | null
  title: string
  available: boolean
  thumb_url: string
  progress?: Progress
  /** Not on the wire yet; the card draws its progress bar only when present. */
  duration_s?: number | null
}

export interface ShowSummary {
  id: number
  title: string
  year: number | null
  /** Poster, else the up-next thumbnail, else null. */
  thumb_url: string | null
  poster_url: string | null
  backdrop_url: string | null
  network?: string | null
  created_at: string
  latest_added_at: string
  season_count: number
  episode_count: number
  available: boolean
  needs_check: boolean
  has_progress: boolean
  collection_ids: number[]
  /** null = caught up. */
  up_next: UpNext | null
}

export type LibraryEntry = ({ kind: 'show' } & ShowSummary) | ({ kind: 'item' } & ItemSummary)

export interface LibraryList {
  total: number
  entries: LibraryEntry[]
}

export interface MissingEpisode {
  kind: 'missing'
  season: number
  number: number
  title: string
  /** YYYY-MM-DD; a future date means unaired. */
  airdate: string | null
  missing: true
}

export type ShowEpisodeEntry = ({ kind: 'item' } & ItemSummary) | MissingEpisode

export interface ShowSeason {
  season: number
  episodes: ShowEpisodeEntry[]
}

export interface ShowDetail extends ShowSummary {
  summary: string | null
  tvmaze_id: number | null
  season_offset: number
  title_locked: boolean
  /** Ascending, specials (season 0) last. */
  seasons: ShowSeason[]
}

export interface ShowPatch {
  title?: string
  confirmed?: true
}

export interface ShowMatchRequest {
  tvmaze_id: number | null
  season_offset?: number
}

export interface MatchCandidate {
  tvmaze_id: number
  name: string
  year: number | null
  network: string | null
  poster_url: string | null
  local_show_id: number | null
}

export interface MatchSearchResponse {
  results: MatchCandidate[]
}

export interface MovieMatchCandidate {
  tmdb_id: number
  title: string
  year: number | null
  poster_url: string | null
}

export interface MovieMatchSearchResponse {
  results: MovieMatchCandidate[]
}

/** `tmdb_id: null` is "Not on TMDB". */
export interface MovieMatchRequest {
  tmdb_id: number | null
}

export interface ShowTrashResponse {
  item_ids: number[]
}

export type CollectionEntryRef = { item_id: number } | { show_id: number }

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

export interface ItemDetail extends EpisodeFields, MovieFields {
  id: number
  type: ItemType
  title: string
  year: number | null
  summary: string | null
  created_at: string
  updated_at: string
  deleted_at: string | null
  collection_ids: number[]
  thumb_url: string
  progress?: Progress
  files: MediaFile[]
}

export interface ItemPatch {
  type?: ItemType
  title?: string
  year?: number
  summary?: string
  /** Only on a real move: it writes an owner alias and locks the item. */
  show_id?: number
  season?: number
  episode?: number
  /** null clears a range; omit to leave it alone. */
  episode_end?: number | null
  confirmed?: true
}

export interface PlayCapabilities {
  containers: string[]
  video_codecs: string[]
  audio_codecs: string[]
  max_height: number
  native_hls: boolean
}

export type QualityRungID = '1080p' | '720p' | '480p' | '360p'

export type Quality = 'original' | 'auto' | 'audio' | QualityRungID

export interface QualityRung {
  id: QualityRungID
  width: number
  height: number
}

export interface PlayRequest {
  file_id?: number
  capabilities: PlayCapabilities
  subtitle_stream_index?: number
  audio_stream_index?: number
  quality?: Quality
}

export interface Subtitle {
  stream_index: number
  lang?: string
  url: string
}

export interface PlayMarkers {
  intro?: { start_s: number; end_s: number }
  credits_start_s?: number
}

export interface PlayResponse {
  mode: 'direct' | 'hls'
  reason:
    | null
    | 'audio_codec'
    | 'video_codec'
    | 'container_not_supported'
    | 'subtitle_burn_in'
    | 'audio_track_selection'
    | 'quality'
  url: string
  session_id?: string
  subtitles: Subtitle[]
  quality: Quality
  qualities: QualityRung[]
  audio_only: boolean
  /** {} when nothing is known. */
  markers: PlayMarkers
}

export interface ProgressUpdate {
  position_s: number
  duration_s: number
  /** Advancing from the Next episode card, or passing credits_start_s. */
  completed?: true
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

export interface TrashJobFileResponse {
  item_id: number
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
  /** match_taken: the local show already holding that TVmaze match. */
  show_id?: number
}
