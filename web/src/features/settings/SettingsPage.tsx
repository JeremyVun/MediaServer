import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import {
  ArrowLeft,
  AlertTriangle,
  ChevronRight,
  Folder,
  FolderOpen,
  HardDrive,
  Info,
  Monitor,
  Moon,
  Plus,
  RefreshCw,
  RotateCcw,
  Sun,
  Trash2,
  Unplug,
} from 'lucide-react'
import { ApiError } from '../../api/client.ts'
import {
  useAddRoot,
  useDetachRoot,
  useFsDirs,
  useHealth,
  useJobs,
  useLibraryItems,
  usePurgeItem,
  usePurgeTrash,
  useRescanRoot,
  useRestoreItem,
  useRetryJob,
  useRoots,
} from '../../api/queries.ts'
import type { ItemSummary, Job, RootInfo } from '../../api/types.ts'
import { formatBytes, formatClock } from '../../lib/format.ts'
import { makeRunOrToast, mutationErrorMessage } from '../../lib/mutationFeedback.ts'
import { useTheme, type ThemePreference } from '../../theme/ThemeProvider.tsx'
import { Button, Card, Dialog, IconButton, Input, Skeleton, useToast } from '../../ui/index.ts'

export function SettingsPage() {
  const roots = useRoots()
  const rescan = useRescanRoot()
  const detach = useDetachRoot()
  const { toast } = useToast()
  const runOrToast = makeRunOrToast(toast)
  const [addOpen, setAddOpen] = useState(false)
  const [detachingRoot, setDetachingRoot] = useState<RootInfo | null>(null)
  const rootList = roots.data ?? []
  const maxFree = Math.max(1, ...rootList.map((root) => (root.online ? root.free_bytes : 0)))

  const onRescan = async (root: RootInfo) => {
    const ok = await runOrToast(() => rescan.mutateAsync(root.id), "Couldn't queue a rescan")
    if (ok) toast({ message: `${root.name} rescan queued` })
  }

  const onDetach = async () => {
    if (!detachingRoot) return
    const root = detachingRoot
    const ok = await runOrToast(() => detach.mutateAsync(root.id), "Couldn't detach root")
    if (!ok) return
    toast({ message: `${root.name} detached` })
    setDetachingRoot(null)
  }

  return (
    <main className="mx-auto max-w-5xl px-4 py-6 sm:px-6 lg:px-8">
      <header className="mb-6 flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="flex items-start gap-3">
          <Link
            to="/"
            aria-label="Back to library"
            className="bg-surface border-line text-primary hover:bg-raised inline-flex size-11 shrink-0 items-center justify-center rounded-md border"
          >
            <ArrowLeft aria-hidden className="size-5" strokeWidth={1.75} />
          </Link>
          <div>
            <h1 className="text-xl font-semibold">Settings</h1>
            <p className="text-sm text-secondary">{rootList.length} roots</p>
          </div>
        </div>
        <Button variant="primary" touch onClick={() => setAddOpen(true)}>
          <Plus aria-hidden className="size-4" strokeWidth={1.75} />
          Add root
        </Button>
      </header>

      <section aria-labelledby="roots-heading" className="space-y-3">
        <div className="flex items-center justify-between">
          <h2 id="roots-heading" className="text-lg font-semibold">
            Roots
          </h2>
        </div>

        {roots.isPending && (
          <div className="space-y-3">
            {Array.from({ length: 3 }, (_, i) => (
              <Skeleton key={i} className="h-28" />
            ))}
          </div>
        )}

        {roots.isError && <EmptyState text="Can't load roots" />}

        {!roots.isPending && !roots.isError && rootList.length === 0 && (
          <EmptyState text="No roots" />
        )}

        {rootList.map((root) => (
          <RootRow
            key={root.id}
            root={root}
            maxFree={maxFree}
            rescanning={rescan.isPending && rescan.variables === root.id}
            onRescan={() => void onRescan(root)}
            onDetach={() => setDetachingRoot(root)}
          />
        ))}
      </section>

      <TrashSection />
      <JobsSection />
      <AppearanceSection />
      <AboutSection />

      {addOpen && <AddRootDialog open={addOpen} onClose={() => setAddOpen(false)} />}

      <Dialog
        open={detachingRoot != null}
        onClose={() => setDetachingRoot(null)}
        title="Detach root?"
        footer={
          <>
            <Button variant="ghost" onClick={() => setDetachingRoot(null)}>
              Cancel
            </Button>
            <Button variant="danger" pending={detach.isPending} onClick={() => void onDetach()}>
              <Unplug aria-hidden className="size-4" strokeWidth={1.75} />
              Detach
            </Button>
          </>
        }
      >
        <p className="text-secondary">
          {detachingRoot?.name} will go offline. Its catalog rows and watch progress stay in place.
        </p>
      </Dialog>
    </main>
  )
}

function TrashSection() {
  const trash = useLibraryItems({ q: '', trashed: true })
  const restore = useRestoreItem()
  const purgeItem = usePurgeItem()
  const purgeTrash = usePurgeTrash()
  const { toast } = useToast()
  const runOrToast = makeRunOrToast(toast)
  const items = trash.data?.pages.flatMap((page) => page.items) ?? []

  const onRestore = async (item: ItemSummary) => {
    const ok = await runOrToast(() => restore.mutateAsync(item.id), "Couldn't restore")
    if (ok) toast({ message: `${item.title} restored` })
  }

  const onPurge = async (item: ItemSummary) => {
    const ok = await runOrToast(() => purgeItem.mutateAsync(item.id), "Couldn't delete")
    if (ok) toast({ message: `${item.title} deleted` })
  }

  const onEmpty = async () => {
    try {
      const res = await purgeTrash.mutateAsync()
      toast({ message: `${res.purged} items deleted${res.skipped ? `, ${res.skipped} skipped` : ''}` })
    } catch (error) {
      toast({ message: mutationErrorMessage(error, "Couldn't empty trash") })
    }
  }

  return (
    <section aria-labelledby="trash-heading" className="mt-10 space-y-3">
      <div className="flex items-center justify-between gap-3">
        <h2 id="trash-heading" className="text-lg font-semibold">
          Trash
        </h2>
        <Button
          variant="danger"
          disabled={items.length === 0}
          pending={purgeTrash.isPending}
          onClick={() => void onEmpty()}
        >
          <Trash2 aria-hidden className="size-4" strokeWidth={1.75} />
          Empty trash
        </Button>
      </div>
      {trash.isPending && <Skeleton className="h-20" />}
      {trash.isError && <EmptyState text="Can't load trash" />}
      {!trash.isPending && items.length === 0 && <EmptyState text="Trash is empty" />}
      {items.map((item) => (
        <Card key={item.id} className="p-4">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
            <div className="min-w-0">
              <h3 className="truncate text-md font-semibold">{item.title}</h3>
              <p className="text-sm text-secondary">
                {[item.year, item.duration_s ? formatClock(item.duration_s) : null]
                  .filter(Boolean)
                  .join(' · ') || 'Pending purge'}
              </p>
            </div>
            <div className="flex shrink-0 flex-wrap gap-2">
              <Button pending={restore.isPending} onClick={() => void onRestore(item)}>
                <RotateCcw aria-hidden className="size-4" strokeWidth={1.75} />
                Restore
              </Button>
              <Button variant="danger" pending={purgeItem.isPending} onClick={() => void onPurge(item)}>
                <Trash2 aria-hidden className="size-4" strokeWidth={1.75} />
                Delete now
              </Button>
            </div>
          </div>
        </Card>
      ))}
    </section>
  )
}

function JobsSection() {
  const jobs = useJobs('failed')
  const retry = useRetryJob()
  const { toast } = useToast()
  const runOrToast = makeRunOrToast(toast)
  const failed = jobs.data ?? []

  const onRetry = async (job: Job) => {
    const ok = await runOrToast(() => retry.mutateAsync(job.id), "Couldn't retry job")
    if (ok) toast({ message: `${job.type} retry queued` })
  }

  return (
    <section aria-labelledby="jobs-heading" className="mt-10 space-y-3">
      <h2 id="jobs-heading" className="text-lg font-semibold">
        Failed jobs
      </h2>
      {jobs.isPending && <Skeleton className="h-20" />}
      {jobs.isError && <EmptyState text="Can't load failed jobs" />}
      {!jobs.isPending && failed.length === 0 && <EmptyState text="No failed jobs" />}
      {failed.map((job) => (
        <Card key={job.id} className="p-4">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
            <div className="min-w-0">
              <div className="mb-1 flex items-center gap-2">
                <AlertTriangle aria-hidden className="text-danger size-4" strokeWidth={1.75} />
                <h3 className="truncate text-md font-semibold">
                  {job.type} #{job.id}
                </h3>
              </div>
              <p className="line-clamp-2 text-sm text-secondary">{job.error ?? job.payload}</p>
            </div>
            <Button pending={retry.isPending && retry.variables === job.id} onClick={() => void onRetry(job)}>
              <RefreshCw aria-hidden className="size-4" strokeWidth={1.75} />
              Retry
            </Button>
          </div>
        </Card>
      ))}
    </section>
  )
}

function AppearanceSection() {
  const theme = useTheme()
  const options: Array<{ value: ThemePreference; label: string; icon: typeof Monitor }> = [
    { value: 'system', label: 'System', icon: Monitor },
    { value: 'dark', label: 'Dark', icon: Moon },
    { value: 'light', label: 'Light', icon: Sun },
  ]

  return (
    <section aria-labelledby="appearance-heading" className="mt-10 space-y-3">
      <h2 id="appearance-heading" className="text-lg font-semibold">
        Appearance
      </h2>
      <div className="flex flex-wrap gap-2">
        {options.map((option) => {
          const Icon = option.icon
          return (
            <button
              key={option.value}
              type="button"
              onClick={() => theme.setPreference(option.value)}
              className={[
                'border-line inline-flex h-10 items-center gap-2 rounded-md border px-3 text-sm',
                theme.preference === option.value
                  ? 'bg-accent-fill text-accent-contrast'
                  : 'bg-surface text-primary hover:bg-raised',
              ].join(' ')}
            >
              <Icon aria-hidden className="size-4" strokeWidth={1.75} />
              {option.label}
            </button>
          )
        })}
      </div>
    </section>
  )
}

function AboutSection() {
  const health = useHealth()
  return (
    <section aria-labelledby="about-heading" className="mt-10 space-y-3">
      <div className="flex items-center gap-2">
        <Info aria-hidden className="text-secondary size-4" strokeWidth={1.75} />
        <h2 id="about-heading" className="text-lg font-semibold">
          About
        </h2>
      </div>
      <dl className="border-line grid gap-4 border-y py-4 sm:grid-cols-3">
        <div>
          <dt className="text-sm text-secondary">Version</dt>
          <dd className="text-md font-medium">{health.data?.version ?? 'Unknown'}</dd>
        </div>
        <div>
          <dt className="text-sm text-secondary">Uptime</dt>
          <dd className="text-md font-medium">{formatClock(health.data?.uptime_s)}</dd>
        </div>
        <div>
          <dt className="text-sm text-secondary">Queue depth</dt>
          <dd className="text-md font-medium">{health.data?.queue_depth ?? 0}</dd>
        </div>
      </dl>
    </section>
  )
}

function RootRow({
  root,
  maxFree,
  rescanning,
  onRescan,
  onDetach,
}: {
  root: RootInfo
  maxFree: number
  rescanning: boolean
  onRescan: () => void
  onDetach: () => void
}) {
  const freePercent = root.online ? Math.round((root.free_bytes / maxFree) * 100) : 0
  return (
    <Card role="region" aria-label={`${root.name} root`} className="p-4">
      <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
        <div className="min-w-0 flex-1">
          <div className="mb-2 flex items-center gap-3">
            <span
              aria-hidden
              className={[
                'inline-flex size-3 rounded-full',
                root.online ? 'bg-success' : 'bg-danger',
              ].join(' ')}
            />
            <div className="min-w-0">
              <h3 className="truncate text-md font-semibold">{root.name}</h3>
              <p className="truncate font-mono text-sm text-secondary">{root.path}</p>
            </div>
          </div>
          <div className="mb-3 flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-secondary">
            <span>{root.online ? 'Online' : 'Offline'}</span>
            <span>{root.file_count} files</span>
            <span>{root.online ? `${formatBytes(root.free_bytes)} free` : 'Free space unavailable'}</span>
          </div>
          <div className="bg-inset h-2 overflow-hidden rounded-full">
            <span
              className={[
                'block h-full rounded-full',
                root.online && root.free_bytes > 0 ? 'bg-info' : 'bg-disabled',
              ].join(' ')}
              style={{ width: `${freePercent}%` }}
            />
          </div>
        </div>
        <div className="flex shrink-0 flex-wrap gap-2">
          <Button disabled={!root.online} pending={rescanning} onClick={onRescan}>
            <RefreshCw aria-hidden className="size-4" strokeWidth={1.75} />
            Rescan
          </Button>
          <Button variant="danger" onClick={onDetach}>
            <Unplug aria-hidden className="size-4" strokeWidth={1.75} />
            Detach
          </Button>
        </div>
      </div>
    </Card>
  )
}

function AddRootDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [path, setPath] = useState('/Volumes')
  const [selectedPath, setSelectedPath] = useState<string | null>(null)
  const [name, setName] = useState('')
  const [inlineError, setInlineError] = useState<string | null>(null)
  const dirs = useFsDirs(path, open)
  const addRoot = useAddRoot()
  const { toast } = useToast()

  const crumbs = useMemo(() => breadcrumbs(path), [path])

  const navigate = (nextPath: string) => {
    setPath(nextPath)
    setSelectedPath(null)
    setInlineError(null)
  }

  const chooseCurrent = () => {
    const current = dirs.data?.path ?? path
    setSelectedPath(current)
    setName(defaultRootName(current))
    setInlineError(null)
  }

  const confirm = async () => {
    if (!selectedPath) return
    try {
      const root = await addRoot.mutateAsync({
        name: name.trim() || defaultRootName(selectedPath),
        path: selectedPath,
      })
      toast({ message: `${root.name} added` })
      onClose()
    } catch (error) {
      setInlineError(rootErrorMessage(error))
    }
  }

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="Add root"
      width="content"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          {selectedPath ? (
            <Button variant="primary" pending={addRoot.isPending} onClick={() => void confirm()}>
              <Plus aria-hidden className="size-4" strokeWidth={1.75} />
              Add root
            </Button>
          ) : (
            <Button variant="primary" disabled={!dirs.data} onClick={chooseCurrent}>
              <FolderOpen aria-hidden className="size-4" strokeWidth={1.75} />
              Choose this folder
            </Button>
          )}
        </>
      }
    >
      <div className="space-y-4">
        <div className="flex items-center gap-2">
          <IconButton
            aria-label="Up one level"
            disabled={!dirs.data?.parent}
            onClick={() => dirs.data?.parent && navigate(dirs.data.parent)}
          >
            <ArrowLeft aria-hidden className="size-5" strokeWidth={1.75} />
          </IconButton>
          <nav aria-label="Folder path" className="flex min-w-0 flex-wrap items-center gap-1 text-sm">
            {crumbs.map((crumb, index) => (
              <span key={crumb.path} className="inline-flex min-w-0 items-center gap-1">
                {index > 0 && (
                  <ChevronRight aria-hidden className="text-tertiary size-4" strokeWidth={1.75} />
                )}
                <button
                  type="button"
                  className="hover:bg-accent-subtle max-w-32 truncate rounded-sm px-2 py-1 text-primary"
                  onClick={() => navigate(crumb.path)}
                >
                  {crumb.label}
                </button>
              </span>
            ))}
          </nav>
        </div>

        <div className="bg-inset border-line max-h-72 overflow-auto rounded-md border">
          {dirs.isPending && (
            <div className="space-y-2 p-3">
              {Array.from({ length: 5 }, (_, i) => (
                <Skeleton key={i} className="h-10" />
              ))}
            </div>
          )}
          {dirs.isError && <div className="p-4 text-sm text-danger">Can't load folder</div>}
          {dirs.data && dirs.data.dirs.length === 0 && (
            <div className="p-4 text-sm text-secondary">No folders</div>
          )}
          {dirs.data?.dirs.map((dir) => (
            <button
              key={dir.path}
              type="button"
              className="border-line flex h-12 w-full items-center gap-3 border-b px-3 text-left last:border-b-0 hover:bg-accent-subtle"
              onClick={() => navigate(dir.path)}
            >
              <Folder aria-hidden className="text-secondary size-5 shrink-0" strokeWidth={1.75} />
              <span className="min-w-0 flex-1 truncate text-primary">{dir.name}</span>
              <ChevronRight aria-hidden className="text-tertiary size-5 shrink-0" strokeWidth={1.75} />
            </button>
          ))}
        </div>

        {selectedPath && (
          <div className="space-y-2">
            <label className="block text-sm font-medium" htmlFor="root-name">
              Root name
            </label>
            <Input
              id="root-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              icon={<HardDrive aria-hidden className="size-4" strokeWidth={1.75} />}
            />
            <p className="truncate font-mono text-sm text-secondary">{selectedPath}</p>
          </div>
        )}

        {inlineError && <p className="text-sm text-danger">{inlineError}</p>}
      </div>
    </Dialog>
  )
}

function EmptyState({ text }: { text: string }) {
  return (
    <div className="flex flex-col items-center gap-3 py-16 text-center">
      <HardDrive aria-hidden className="text-tertiary size-10" strokeWidth={1.75} />
      <p className="text-md">{text}</p>
    </div>
  )
}

function breadcrumbs(path: string): { label: string; path: string }[] {
  if (path === '/') return [{ label: '/', path: '/' }]
  const parts = path.split('/').filter(Boolean)
  const crumbs = [{ label: '/', path: '/' }]
  let current = ''
  for (const part of parts) {
    current += `/${part}`
    crumbs.push({ label: part, path: current })
  }
  return crumbs
}

function defaultRootName(path: string): string {
  const parts = path.split('/').filter(Boolean)
  return parts.at(-1) ?? path
}

function rootErrorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === 'duplicate_root') return 'This folder overlaps an attached root.'
    if (error.code === 'path_not_found') return 'This folder is no longer available.'
    if (error.code === 'path_not_absolute') return 'Choose an absolute folder path.'
    return error.message
  }
  return 'Adding the root failed.'
}
