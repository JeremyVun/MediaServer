import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { Link } from 'react-router'
import { CheckCircle2, ChevronDown, ChevronUp, Loader2, RotateCcw, Upload, X } from 'lucide-react'
import { ApiError, api, jsonApi } from '../../api/client.ts'
import { useRoots } from '../../api/queries.ts'
import type {
  CreateUploadRequest,
  CreateUploadResponse,
  RootInfo,
  UploadChunkResponse,
  UploadCompleteEvent,
  UploadCompleteResponse,
  UploadProgressEvent,
  UploadStatus,
} from '../../api/types.ts'
import { formatBytes } from '../../lib/format.ts'
import { Button, IconButton, useToast } from '../../ui/index.ts'

type UploadRowStatus =
  | 'queued'
  | 'uploading'
  | 'needs_file'
  | 'processing'
  | 'complete'
  | 'error'
  | 'aborted'

interface UploadRow {
  key: string
  uploadId?: string
  file?: File
  name: string
  size: number
  rootId: number
  received: number
  status: UploadRowStatus
  speedBps?: number
  error?: string
  itemId?: number
}

interface PersistedUpload {
  uploadId: string
  name: string
  size: number
  rootId: number
  received: number
  status: 'needs_file' | 'processing'
}

interface UploadContextValue {
  addFiles: (files: FileList | File[]) => void
}

const UploadContext = createContext<UploadContextValue | null>(null)
const STORAGE_KEY = 'media-server.uploads.v1'

export function UploadProvider({ children }: { children: ReactNode }) {
  const roots = useRoots()
  const { toast } = useToast()
  const [rows, setRows] = useState<UploadRow[]>(loadPersistedUploads)
  const [collapsed, setCollapsed] = useState(false)
  const [preferredRootId, setPreferredRootId] = useState<number | null>(null)
  const rowsRef = useRef(rows)
  const controllersRef = useRef(new Map<string, AbortController>())

  const onlineRoots = useMemo(
    () => (roots.data ?? []).filter((root) => root.online),
    [roots.data],
  )
  const selectedRootId = useMemo(() => {
    const preferred = onlineRoots.find((root) => root.id === preferredRootId)
    return (preferred ?? mostFreeRoot(onlineRoots))?.id ?? null
  }, [onlineRoots, preferredRootId])

  useEffect(() => {
    rowsRef.current = rows
  }, [rows])

  useEffect(() => {
    persistUploads(rows)
  }, [rows])

  const updateRow = useCallback((key: string, patch: Partial<UploadRow>) => {
    setRows((current) => current.map((row) => (row.key === key ? { ...row, ...patch } : row)))
  }, [])

  const runUpload = useCallback(
    async (key: string, file: File, rootId: number, existingUploadId?: string) => {
      let uploadId = existingUploadId
      let offset = rowsRef.current.find((row) => row.key === key)?.received ?? 0
      let chunkSize = 8 * 1024 * 1024
      let lastBytes = offset
      let lastAt = performance.now()

      try {
        updateRow(key, { status: 'uploading', error: undefined, speedBps: undefined })
        if (uploadId) {
          const status = await api<UploadStatus>(`/api/uploads/${uploadId}`)
          if (status.status !== 'active') {
            updateRow(key, {
              received: status.received,
              status: status.status === 'complete' ? 'processing' : 'aborted',
            })
            return
          }
          offset = status.received
          if (status.chunk_size > 0) chunkSize = status.chunk_size
          updateRow(key, { received: offset })
        } else {
          const request: CreateUploadRequest = {
            filename: file.name,
            size: file.size,
            root_id: rootId,
          }
          const created = await jsonApi<CreateUploadResponse>('/api/uploads', 'POST', request)
          uploadId = created.id
          if (created.chunk_size > 0) chunkSize = created.chunk_size
          updateRow(key, { uploadId, received: 0 })
        }

        while (offset < file.size) {
          const endExclusive = Math.min(offset + chunkSize, file.size)
          const controller = new AbortController()
          controllersRef.current.set(key, controller)
          try {
            const chunk = file.slice(offset, endExclusive)
            const response = await api<UploadChunkResponse>(`/api/uploads/${uploadId}`, {
              method: 'PUT',
              headers: {
                'Content-Range': `bytes ${offset}-${endExclusive - 1}/${file.size}`,
              },
              body: chunk,
              signal: controller.signal,
            })
            const now = performance.now()
            const elapsed = Math.max((now - lastAt) / 1000, 0.001)
            const speedBps = (response.received - lastBytes) / elapsed
            offset = response.received
            lastBytes = offset
            lastAt = now
            updateRow(key, { received: offset, speedBps, status: 'uploading' })
          } catch (error) {
            if (error instanceof ApiError && error.code === 'offset_mismatch' && uploadId) {
              const status = await api<UploadStatus>(`/api/uploads/${uploadId}`)
              offset = status.received
              lastBytes = offset
              lastAt = performance.now()
              updateRow(key, { received: offset })
              continue
            }
            throw error
          } finally {
            controllersRef.current.delete(key)
          }
        }

        updateRow(key, { status: 'processing', received: file.size, speedBps: undefined })
        const complete = await api<UploadCompleteResponse>(`/api/uploads/${uploadId}/complete`, {
          method: 'POST',
        })
        if (complete.item_id != null) {
          updateRow(key, { status: 'complete', itemId: complete.item_id })
        }
      } catch (error) {
        controllersRef.current.delete(key)
        if (isAbortError(error)) {
          updateRow(key, { status: 'aborted', speedBps: undefined })
          return
        }
        const message = error instanceof Error ? error.message : 'Upload failed'
        updateRow(key, { status: 'error', error: message, speedBps: undefined })
      }
    },
    [updateRow],
  )

  const addFiles = useCallback(
    (files: FileList | File[]) => {
      const fileList = Array.from(files)
      if (fileList.length === 0) return
      const rootId = selectedRootId ?? mostFreeRoot(onlineRoots)?.id
      if (!rootId) {
        toast({ message: 'No online root is available' })
        return
      }
      setCollapsed(false)
      for (const file of fileList) {
        const resumable = rowsRef.current.find(
          (row) =>
            row.uploadId &&
            row.name === file.name &&
            row.size === file.size &&
            (row.status === 'needs_file' || row.status === 'error'),
        )
        if (resumable) {
          updateRow(resumable.key, {
            file,
            rootId: resumable.rootId,
            status: 'queued',
            error: undefined,
          })
          void runUpload(resumable.key, file, resumable.rootId, resumable.uploadId)
          continue
        }
        const key = randomRowKey()
        const row: UploadRow = {
          key,
          file,
          name: file.name,
          size: file.size,
          rootId,
          received: 0,
          status: 'queued',
        }
        setRows((current) => [...current, row])
        void runUpload(key, file, rootId)
      }
    },
    [onlineRoots, runUpload, selectedRootId, toast, updateRow],
  )

  const cancelUpload = useCallback(
    (key: string) => {
      const row = rowsRef.current.find((candidate) => candidate.key === key)
      controllersRef.current.get(key)?.abort()
      controllersRef.current.delete(key)
      if (row?.uploadId && row.status !== 'complete' && row.status !== 'processing') {
        void api<void>(`/api/uploads/${row.uploadId}`, { method: 'DELETE' }).catch(() => undefined)
      }
      updateRow(key, { status: 'aborted', speedBps: undefined })
    },
    [updateRow],
  )

  const removeUpload = useCallback((key: string) => {
    setRows((current) => current.filter((row) => row.key !== key))
  }, [])

  const resumeUpload = useCallback(
    (key: string, file: File) => {
      const row = rowsRef.current.find((candidate) => candidate.key === key)
      if (!row || !row.uploadId) return
      if (row.name !== file.name || row.size !== file.size) {
        toast({ message: 'Selected file does not match the upload' })
        return
      }
      updateRow(key, { file, status: 'queued', error: undefined })
      void runUpload(key, file, row.rootId, row.uploadId)
    },
    [runUpload, toast, updateRow],
  )

  useEffect(() => {
    const onProgress = (event: Event) => {
      const detail = (event as CustomEvent<UploadProgressEvent>).detail
      if (!detail) return
      setRows((current) =>
        current.map((row) =>
          row.uploadId === detail.id
            ? { ...row, received: detail.received, size: detail.size }
            : row,
        ),
      )
    }
    const onComplete = (event: Event) => {
      const detail = (event as CustomEvent<UploadCompleteEvent>).detail
      if (!detail) return
      setRows((current) =>
        current.map((row) => {
          if (row.uploadId !== detail.id) return row
          if (detail.item_id == null) return { ...row, status: 'processing', speedBps: undefined }
          return { ...row, status: 'complete', itemId: detail.item_id, speedBps: undefined }
        }),
      )
    }
    window.addEventListener('media-server:upload-progress', onProgress)
    window.addEventListener('media-server:upload-complete', onComplete)
    return () => {
      window.removeEventListener('media-server:upload-progress', onProgress)
      window.removeEventListener('media-server:upload-complete', onComplete)
    }
  }, [])

  const value = useMemo<UploadContextValue>(() => ({ addFiles }), [addFiles])

  return (
    <UploadContext.Provider value={value}>
      {children}
      <UploadTray
        rows={rows}
        roots={onlineRoots}
        selectedRootId={selectedRootId}
        onSelectRoot={setPreferredRootId}
        collapsed={collapsed}
        onToggleCollapsed={() => setCollapsed((value) => !value)}
        onCancel={cancelUpload}
        onRemove={removeUpload}
        onResume={resumeUpload}
      />
    </UploadContext.Provider>
  )
}

function UploadTray({
  rows,
  roots,
  selectedRootId,
  onSelectRoot,
  collapsed,
  onToggleCollapsed,
  onCancel,
  onRemove,
  onResume,
}: {
  rows: UploadRow[]
  roots: RootInfo[]
  selectedRootId: number | null
  onSelectRoot: (id: number) => void
  collapsed: boolean
  onToggleCollapsed: () => void
  onCancel: (key: string) => void
  onRemove: (key: string) => void
  onResume: (key: string, file: File) => void
}) {
  if (rows.length === 0) return null
  const activeCount = rows.filter((row) => row.status === 'uploading' || row.status === 'queued').length

  return (
    <aside className="fixed right-4 bottom-4 z-[var(--z-tray)] w-[min(28rem,calc(100vw-2rem))]">
      <div className="bg-raised border-line shadow-overlay overflow-hidden rounded-md border">
        <div className="flex items-center gap-3 border-b border-line px-3 py-2">
          <Upload aria-hidden className="text-accent size-5 shrink-0" strokeWidth={1.75} />
          <div className="min-w-0 flex-1">
            <h2 className="truncate text-base font-semibold">Uploads</h2>
            <p className="truncate text-sm text-secondary">
              {activeCount > 0 ? `${activeCount} active` : `${rows.length} queued`}
            </p>
          </div>
          {roots.length > 1 && (
            <select
              aria-label="Upload root"
              value={selectedRootId ?? ''}
              onChange={(event) => onSelectRoot(Number(event.target.value))}
              className="bg-inset border-line-strong text-primary h-9 max-w-40 rounded-sm border px-2 text-sm"
            >
              {roots.map((root) => (
                <option key={root.id} value={root.id}>
                  {root.name}
                </option>
              ))}
            </select>
          )}
          <IconButton aria-label={collapsed ? 'Expand uploads' : 'Collapse uploads'} onClick={onToggleCollapsed}>
            {collapsed ? (
              <ChevronUp aria-hidden className="size-5" strokeWidth={1.75} />
            ) : (
              <ChevronDown aria-hidden className="size-5" strokeWidth={1.75} />
            )}
          </IconButton>
        </div>
        {!collapsed && (
          <div className="max-h-[min(60vh,28rem)] overflow-y-auto">
            {rows.map((row) => (
              <UploadRowView
                key={row.key}
                row={row}
                root={roots.find((candidate) => candidate.id === row.rootId)}
                onCancel={() => onCancel(row.key)}
                onRemove={() => onRemove(row.key)}
                onResume={(file) => onResume(row.key, file)}
              />
            ))}
          </div>
        )}
      </div>
    </aside>
  )
}

function UploadRowView({
  row,
  root,
  onCancel,
  onRemove,
  onResume,
}: {
  row: UploadRow
  root?: RootInfo
  onCancel: () => void
  onRemove: () => void
  onResume: (file: File) => void
}) {
  const inputRef = useRef<HTMLInputElement | null>(null)
  const percent = row.size > 0 ? Math.max(0, Math.min(100, (row.received / row.size) * 100)) : 0
  const done = row.status === 'complete' || row.status === 'aborted'
  const removable = done || row.status === 'error'

  return (
    <div className="border-b border-line px-3 py-3 last:border-b-0">
      <div className="flex items-start gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <p className="min-w-0 flex-1 truncate text-sm font-medium">{row.name}</p>
            {row.status === 'complete' && (
              <CheckCircle2 aria-hidden className="text-success size-4 shrink-0" strokeWidth={1.75} />
            )}
            {row.status === 'uploading' && (
              <Loader2 aria-hidden className="text-accent size-4 shrink-0 animate-spin" strokeWidth={1.75} />
            )}
          </div>
          <p className="mt-1 truncate text-xs text-secondary">
            {rowStatusText(row, root)} · {formatBytes(row.received)} / {formatBytes(row.size)}
          </p>
          <div className="bg-progress-track mt-2 h-2 overflow-hidden rounded-full">
            <div className="bg-accent-fill h-full rounded-full" style={{ width: `${percent}%` }} />
          </div>
          {row.status === 'error' && row.error && (
            <p className="mt-2 line-clamp-2 text-xs text-danger">{row.error}</p>
          )}
          <div className="mt-2 flex items-center gap-2">
            {row.status === 'needs_file' && (
              <>
                <Button variant="secondary" className="h-8 px-3 text-sm" onClick={() => inputRef.current?.click()}>
                  <RotateCcw aria-hidden className="size-4" strokeWidth={1.75} />
                  Resume
                </Button>
                <input
                  ref={inputRef}
                  type="file"
                  aria-label="Choose file to resume upload"
                  className="sr-only"
                  onChange={(event) => {
                    const file = event.target.files?.[0]
                    if (file) onResume(file)
                    event.currentTarget.value = ''
                  }}
                />
              </>
            )}
            {row.itemId && (
              <Link
                to={`/items/${row.itemId}`}
                className="text-accent hover:text-accent-hover rounded-sm text-sm font-semibold"
              >
                Open item
              </Link>
            )}
          </div>
        </div>
        <IconButton
          aria-label={removable ? 'Remove upload' : 'Cancel upload'}
          onClick={removable ? onRemove : onCancel}
          className="size-9"
        >
          <X aria-hidden className="size-4" strokeWidth={1.75} />
        </IconButton>
      </div>
    </div>
  )
}

function rowStatusText(row: UploadRow, root?: RootInfo): string {
  switch (row.status) {
    case 'queued':
      return `Queued for ${root?.name ?? 'root'}`
    case 'uploading':
      return row.speedBps ? `Uploading at ${formatBytes(row.speedBps)}/s` : 'Uploading'
    case 'needs_file':
      return 'Waiting for file'
    case 'processing':
      return 'Processing...'
    case 'complete':
      return 'Complete'
    case 'aborted':
      return 'Canceled'
    case 'error':
      return 'Failed'
  }
}

function mostFreeRoot(roots: RootInfo[]): RootInfo | undefined {
  return roots.reduce<RootInfo | undefined>((best, root) => {
    if (!best || root.free_bytes > best.free_bytes) return root
    return best
  }, undefined)
}

function loadPersistedUploads(): UploadRow[] {
  if (typeof window === 'undefined') return []
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY)
    if (!raw) return []
    const parsed = JSON.parse(raw) as PersistedUpload[]
    return parsed.map((upload) => ({
      key: randomRowKey(),
      uploadId: upload.uploadId,
      name: upload.name,
      size: upload.size,
      rootId: upload.rootId,
      received: upload.received,
      status: upload.status,
    }))
  } catch {
    return []
  }
}

function persistUploads(rows: UploadRow[]) {
  const persisted: PersistedUpload[] = rows
    .filter((row) => row.uploadId && (row.status === 'needs_file' || row.status === 'processing' || row.status === 'uploading' || row.status === 'queued' || row.status === 'error'))
    .map((row) => ({
      uploadId: row.uploadId!,
      name: row.name,
      size: row.size,
      rootId: row.rootId,
      received: row.received,
      status: row.status === 'processing' ? 'processing' : 'needs_file',
    }))
  if (persisted.length === 0) {
    window.localStorage.removeItem(STORAGE_KEY)
    return
  }
  window.localStorage.setItem(STORAGE_KEY, JSON.stringify(persisted))
}

function randomRowKey(): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) return crypto.randomUUID()
  return `${Date.now()}-${Math.random().toString(16).slice(2)}`
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError'
}

// eslint-disable-next-line react-refresh/only-export-components
export function useUploads(): UploadContextValue {
  const ctx = useContext(UploadContext)
  if (!ctx) throw new Error('useUploads must be used inside UploadProvider')
  return ctx
}
