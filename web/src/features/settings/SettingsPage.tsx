import { useMemo, useState, type ReactNode } from 'react'
import { Link } from 'react-router'
import {
  ArrowLeft,
  AlertTriangle,
  Check,
  ChevronRight,
  Folder,
  FolderOpen,
  HardDrive,
  Plus,
  RefreshCw,
  RotateCcw,
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
import { useTheme } from '../../theme/ThemeProvider.tsx'
import { CARD_STYLES, setCardStyle, useCardStyle, type CardStyle } from '../../theme/cardStyle.ts'
import { THEMES, type ThemeSwatch } from '../../theme/registry.ts'
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
  const onlineRoots = rootList.filter((root) => root.online)
  const totalFiles = rootList.reduce((sum, root) => sum + root.file_count, 0)
  const totalFree = onlineRoots.reduce((sum, root) => sum + root.free_bytes, 0)
  const maxFree = Math.max(1, ...rootList.map((root) => (root.online ? root.free_bytes : 0)))

  // A quiet status line under the title: roots, cataloged files, free space.
  const summary = roots.isPending
    ? 'Loading roots…'
    : [
        `${rootList.length} ${rootList.length === 1 ? 'root' : 'roots'}`,
        totalFiles > 0 ? `${totalFiles} ${totalFiles === 1 ? 'file' : 'files'}` : null,
        onlineRoots.length > 0 ? `${formatBytes(totalFree)} free` : null,
      ]
        .filter(Boolean)
        .join(' · ')

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
    <main className="mx-auto max-w-7xl px-4 py-6 sm:px-6 lg:px-8">
      <header className="mb-8 flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
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
            <p className="text-sm text-secondary">{summary}</p>
          </div>
        </div>
        <Button variant="primary" touch onClick={() => setAddOpen(true)}>
          <Plus aria-hidden className="size-4" strokeWidth={1.75} />
          Add root
        </Button>
      </header>

      {/* Two columns on large screens: Appearance (wide — the theme grid wants
          the room) beside a management rail. The rail is a fixed narrow width,
          so a single root reads as an intentional card, not a stretched banner.
          Everything inside the rail stacks vertically regardless of viewport —
          Tailwind's sm:/lg: breakpoints track the window, not this column, so
          the usual "go wide" rules would misfire in a ~22rem rail. */}
      <div className="grid gap-8 lg:grid-cols-[minmax(0,1fr)_22rem] lg:items-start lg:gap-x-0">
        <div className="min-w-0 lg:pr-8">
          <AppearanceSection />
        </div>

        {/* A hairline defines the rail as a sidebar, so its list of sections
            reading longer than Appearance looks intentional, not lopsided. */}
        <div className="space-y-8 lg:border-line lg:border-l lg:pl-8">
          <section aria-labelledby="roots-heading" className="space-y-3">
            <h2 id="roots-heading" className="text-lg font-semibold">
              Roots
            </h2>

            {roots.isPending && (
              <div className="space-y-3">
                {Array.from({ length: 2 }, (_, i) => (
                  <Skeleton key={i} className="h-32" />
                ))}
              </div>
            )}

            {roots.isError && <EmptyState text="Can't load roots" />}

            {!roots.isPending && !roots.isError && rootList.length === 0 && (
              <EmptyState text="No roots yet" />
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
          <AboutSection />
        </div>
      </div>

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
    <section aria-labelledby="trash-heading" className="space-y-3">
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
          <div className="flex flex-col gap-3">
            <div className="min-w-0">
              <h3 className="truncate text-md font-semibold">{item.title}</h3>
              <p className="text-sm text-secondary">
                {[item.year, item.duration_s ? formatClock(item.duration_s) : null]
                  .filter(Boolean)
                  .join(' · ') || 'Pending purge'}
              </p>
            </div>
            <div className="flex flex-wrap gap-2">
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
    <section aria-labelledby="jobs-heading" className="space-y-3">
      <h2 id="jobs-heading" className="text-lg font-semibold">
        Failed jobs
      </h2>
      {jobs.isPending && <Skeleton className="h-20" />}
      {jobs.isError && <EmptyState text="Can't load failed jobs" />}
      {!jobs.isPending && failed.length === 0 && <EmptyState text="No failed jobs" />}
      {failed.map((job) => (
        <Card key={job.id} className="p-4">
          <div className="flex flex-col gap-3">
            <div className="min-w-0">
              <div className="mb-1 flex items-center gap-2">
                <AlertTriangle aria-hidden className="text-danger size-4" strokeWidth={1.75} />
                <h3 className="truncate text-md font-semibold">
                  {job.type} #{job.id}
                </h3>
              </div>
              <p className="line-clamp-2 text-sm text-secondary">{job.error ?? job.payload}</p>
            </div>
            <Button
              className="self-start"
              pending={retry.isPending && retry.variables === job.id}
              onClick={() => void onRetry(job)}
            >
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
  const cardStyle = useCardStyle()

  return (
    <section aria-labelledby="appearance-heading" className="space-y-6">
      <h2 id="appearance-heading" className="text-lg font-semibold">
        Appearance
      </h2>

      <div className="space-y-2">
        <h3 className="text-sm font-medium text-secondary">Theme</h3>
        {/* Swatch tiles come from the registry, so a new theme file appears
            here automatically. 'System' follows the OS light/dark setting.
            auto-fill keeps a steady tile size whether this column is full-width
            (mobile) or the wide half of the desktop split. */}
        <div className="grid grid-cols-[repeat(auto-fill,minmax(6.25rem,1fr))] gap-2">
          <ThemeTile
            label="System"
            selected={theme.preference === 'system'}
            onClick={() => theme.setPreference('system')}
          >
            <SystemPreview />
          </ThemeTile>
          {THEMES.map((option) => (
            <ThemeTile
              key={option.value}
              label={option.label}
              selected={theme.preference === option.value}
              onClick={() => theme.setPreference(option.value)}
            >
              <SwatchPreview swatch={option.swatch} />
            </ThemeTile>
          ))}
        </div>
      </div>

      <div className="space-y-2">
        <h3 className="text-sm font-medium text-secondary">Card style</h3>
        {/* Mirrors the theme tiles: each option previews the library card layout
            it produces, rendered in the live theme's own tokens. */}
        <div className="grid max-w-[16rem] grid-cols-2 gap-2">
          {CARD_STYLES.map((option) => (
            <ThemeTile
              key={option.value}
              label={option.label}
              selected={cardStyle === option.value}
              onClick={() => setCardStyle(option.value)}
            >
              <CardStylePreview style={option.value} />
            </ThemeTile>
          ))}
        </div>
      </div>
    </section>
  )
}

// Miniature of a library card in each style, so the choice is shown, not
// described. Uses live theme utilities (not inline colours) — unlike the theme
// swatches, this always renders in the currently active theme.
function CardStylePreview({ style }: { style: CardStyle }) {
  if (style === 'minimal') {
    // The whole tile is poster; the title sits over a scrim at its foot.
    return (
      <span className="absolute inset-0 bg-canvas">
        <span className="bg-raised absolute inset-[12%] overflow-hidden rounded-[3px]">
          <span className="absolute inset-x-0 bottom-0 h-[48%] bg-gradient-to-t from-black/80 to-transparent" />
          <span className="absolute bottom-[14%] left-[12%] h-[11%] w-[62%] rounded-full bg-white/90" />
        </span>
      </span>
    )
  }
  // Compact: poster on top, title + metadata line beneath it on the canvas.
  return (
    <span className="absolute inset-0 bg-canvas">
      <span className="absolute inset-x-[12%] top-[12%] bottom-[12%]">
        <span className="bg-raised absolute inset-x-0 top-0 h-[58%] rounded-[3px]" />
        <span className="bg-primary absolute left-0 top-[70%] h-[10%] w-[72%] rounded-full" />
        <span className="bg-tertiary absolute left-0 top-[87%] h-[8%] w-[46%] rounded-full" />
      </span>
    </span>
  )
}

// A theme choice rendered as a mini app preview using the theme's own colours.
function ThemeTile({
  label,
  selected,
  onClick,
  children,
}: {
  label: string
  selected: boolean
  onClick: () => void
  children: ReactNode
}) {
  return (
    <button
      type="button"
      aria-pressed={selected}
      aria-label={label}
      onClick={onClick}
      className={[
        'flex flex-col items-center gap-1.5 rounded-md border p-1.5 transition-colors',
        selected ? 'border-accent bg-accent-subtle' : 'border-line hover:bg-raised',
      ].join(' ')}
    >
      <span className="border-line relative block aspect-[4/3] w-full overflow-hidden rounded-md border">
        {children}
        {selected && (
          <span className="bg-accent-fill text-on-accent absolute right-1 top-1 inline-flex size-4 items-center justify-center rounded-full">
            <Check aria-hidden className="size-3" strokeWidth={3} />
          </span>
        )}
      </span>
      <span
        className={[
          'line-clamp-2 min-h-8 w-full text-center text-xs leading-tight',
          selected ? 'text-primary font-medium' : 'text-secondary',
        ].join(' ')}
      >
        {label}
      </span>
    </button>
  )
}

// A theme's canvas + a surface "card" with two text lines and an accent chip.
// Uses inline colours (not utilities) because a swatch shows a theme other
// than the active one, whose tokens aren't live on the page.
function SwatchPreview({ swatch }: { swatch: ThemeSwatch }) {
  return (
    <span className="absolute inset-0" style={{ background: swatch.canvas }}>
      <span
        className="absolute inset-x-[14%] bottom-[18%] top-[18%] rounded-[3px]"
        style={{ background: swatch.surface }}
      >
        <span
          className="absolute left-[12%] top-[18%] h-[10%] w-[54%] rounded-full"
          style={{ background: swatch.text }}
        />
        <span
          className="absolute left-[12%] top-[40%] h-[8%] w-[34%] rounded-full opacity-40"
          style={{ background: swatch.text }}
        />
        <span
          className="absolute bottom-[14%] left-[12%] h-[22%] w-[30%] rounded-[2px]"
          style={{ background: swatch.accent }}
        />
      </span>
    </span>
  )
}

// 'System' follows the OS: a diagonal split of the built-in dark and light
// canvases — the familiar auto-appearance signifier.
function SystemPreview() {
  const dark = THEMES.find((t) => t.value === 'dark')?.swatch.canvas ?? '#0c0d10'
  const light = THEMES.find((t) => t.value === 'light')?.swatch.canvas ?? '#f7f6f3'
  return (
    <span
      className="absolute inset-0"
      style={{ background: `linear-gradient(115deg, ${dark} 0 50%, ${light} 50% 100%)` }}
    />
  )
}

function AboutSection() {
  const health = useHealth()
  return (
    <section aria-labelledby="about-heading" className="space-y-3">
      <h2 id="about-heading" className="text-lg font-semibold">
        About
      </h2>
      <dl className="border-line overflow-hidden rounded-lg border">
        <AboutRow label="Version" value={health.data?.version ?? 'Unknown'} />
        <AboutRow label="Uptime" value={formatClock(health.data?.uptime_s)} />
        <AboutRow label="Queue depth" value={String(health.data?.queue_depth ?? 0)} />
      </dl>
    </section>
  )
}

function AboutRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="border-line flex items-center justify-between gap-4 border-b px-4 py-3 last:border-b-0">
      <dt className="text-sm text-secondary">{label}</dt>
      <dd className="text-md truncate font-medium tabular-nums">{value}</dd>
    </div>
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
      <div className="flex flex-col gap-4">
        <div className="min-w-0">
          <div className="mb-2 flex items-center gap-3">
            <span
              aria-hidden
              className={[
                'inline-flex size-3 shrink-0 rounded-full',
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
        <div className="flex flex-wrap gap-2">
          <Button disabled={!root.online} pending={rescanning} onClick={onRescan}>
            <RefreshCw aria-hidden className="size-4" strokeWidth={1.75} />
            Rescan
          </Button>
          <Button variant="secondary" onClick={onDetach}>
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

// Rail-sized empty/error state: a quiet dashed panel with one line of copy.
function EmptyState({ text }: { text: string }) {
  return (
    <div className="border-line text-secondary flex items-center gap-2 rounded-lg border border-dashed px-4 py-5 text-sm">
      <HardDrive aria-hidden className="text-tertiary size-4 shrink-0" strokeWidth={1.75} />
      <span>{text}</span>
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
