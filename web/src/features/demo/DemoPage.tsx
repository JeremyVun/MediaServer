import { useState } from 'react'
import { ChevronDown, ChevronLeft, ChevronRight, Info, MoreHorizontal, Play, Plus, Search, Trash2 } from 'lucide-react'
import {
  Button,
  Card,
  Dialog,
  IconButton,
  Input,
  Menu,
  MenuItem,
  MenuSeparator,
  Skeleton,
  useToast,
} from '../../ui/index.ts'
import { useTheme, type ThemePreference } from '../../theme/ThemeProvider.tsx'

/**
 * Storybook-less demo route (/demo): every ui/ primitive rendered in both
 * themes side by side, plus the live app theme switcher. This page is the
 * M1 acceptance surface — nothing here may use a hardcoded color.
 */
export function DemoPage() {
  const { preference, setPreference } = useTheme()

  return (
    <main className="mx-auto max-w-7xl px-6 py-10">
      <header className="mb-8 flex flex-wrap items-center justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">Design system</h1>
          <p className="text-sm text-secondary mt-1">
            ui/ primitives rendered in both themes · app theme:
          </p>
        </div>
        <div className="flex gap-2">
          {(['system', 'dark', 'light'] as ThemePreference[]).map((pref) => (
            <Button
              key={pref}
              variant={preference === pref ? 'primary' : 'secondary'}
              onClick={() => setPreference(pref)}
            >
              {pref}
            </Button>
          ))}
        </div>
      </header>

      <div className="grid gap-6 lg:grid-cols-2">
        <ThemePanel theme="dark" />
        <ThemePanel theme="light" />
      </div>
    </main>
  )
}

function ThemePanel({ theme }: { theme: 'dark' | 'light' }) {
  return (
    <section
      data-theme={theme}
      className="bg-canvas text-primary border-line rounded-lg border p-6"
    >
      <h2 className="text-lg mb-6 font-semibold tracking-tight capitalize">{theme}</h2>
      <div className="flex flex-col gap-8">
        <ButtonsDemo />
        <InputsDemo />
        <CardsDemo />
        <MenusDemo />
        <SkeletonsDemo />
        <DialogAndToastDemo />
      </div>
    </section>
  )
}

function DemoBlock({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <h3 className="text-xs text-tertiary mb-3 font-medium tracking-wide uppercase">{label}</h3>
      {children}
    </div>
  )
}

function ButtonsDemo() {
  return (
    <DemoBlock label="Buttons">
      <div className="flex flex-wrap items-center gap-3">
        <Button variant="primary">
          <Play aria-hidden className="size-4 fill-current" strokeWidth={1.75} />
          Play
        </Button>
        <Button variant="secondary">Add to collection</Button>
        <Button variant="ghost">Cancel</Button>
        <Button variant="danger">Delete</Button>
        <Button variant="primary" pending>
          Saving
        </Button>
        <Button variant="secondary" disabled>
          Disabled
        </Button>
        <IconButton aria-label="Search">
          <Search aria-hidden className="size-5" strokeWidth={1.75} />
        </IconButton>
        <Button variant="primary" touch>
          Touch primary (44px)
        </Button>
      </div>
    </DemoBlock>
  )
}

function InputsDemo() {
  return (
    <DemoBlock label="Inputs">
      <div className="flex max-w-md flex-col gap-3">
        <Input
          type="search"
          placeholder="Search your library"
          aria-label="Search your library"
          icon={<Search className="size-4" strokeWidth={1.75} />}
        />
        <Input placeholder="Title" aria-label="Title" defaultValue="Big Buck Bunny" />
        <Input placeholder="Disabled" aria-label="Disabled input" disabled />
      </div>
    </DemoBlock>
  )
}

function CardsDemo() {
  return (
    <DemoBlock label="Cards">
      <div className="grid grid-cols-2 gap-4 sm:grid-cols-3">
        <Card interactive className="cursor-pointer">
          <div className="bg-inset relative flex aspect-[2/3] items-center justify-center">
            <Play aria-hidden className="text-tertiary size-8 fill-current" strokeWidth={1.75} />
            {/* Watch-progress bar flush along the image's bottom edge */}
            <div className="bg-progress-track absolute inset-x-0 bottom-0 h-[3px]">
              <div className="bg-accent h-full w-1/3" />
            </div>
          </div>
          <div className="p-3">
            <p className="text-md truncate font-medium">Big Buck Bunny</p>
            <p className="text-sm text-secondary">2008</p>
          </div>
        </Card>
        <Card>
          <div className="p-3">
            <p className="text-md mb-1 font-medium">Static card</p>
            <p className="text-sm text-secondary">
              Metadata in secondary, <span className="font-mono">h264</span> and paths in mono.
            </p>
            <p className="text-xs text-tertiary tabular mt-2">00:32:05 · 1.4 GB</p>
          </div>
        </Card>
        <Card className="p-3">
          <span className="bg-accent-subtle text-accent inline-block rounded-sm px-2 py-0.5 text-xs font-medium">
            New
          </span>
          <p className="text-sm text-secondary mt-2">Badge on accent-subtle.</p>
        </Card>
      </div>
    </DemoBlock>
  )
}

function MenusDemo() {
  const { toast } = useToast()
  const [speed, setSpeed] = useState('1×')
  const collections = ['Euphoria', 'Movies', 'Norsemen']
  const [memberships, setMemberships] = useState<string[]>(['Movies'])
  const [filter, setFilter] = useState('')

  return (
    <DemoBlock label="Menus">
      <div className="flex flex-wrap items-center gap-3">
        <Menu
          aria-label="Open item menu"
          trigger={<MoreHorizontal aria-hidden className="size-5" strokeWidth={1.75} />}
          triggerClassName="bg-raised/90 text-primary border-line flex size-9 cursor-pointer items-center justify-center rounded-sm border shadow-raised"
        >
          {({ view, setView }) =>
            view === 'collections' ? (
              <div className="w-56">
                <MenuItem
                  icon={<ChevronLeft className="size-4" strokeWidth={1.75} />}
                  closeOnSelect={false}
                  onSelect={() => setView(null)}
                >
                  Back
                </MenuItem>
                <div className="p-1">
                  <Input
                    aria-label="Filter collections"
                    placeholder="Filter"
                    value={filter}
                    onChange={(e) => setFilter(e.target.value)}
                  />
                </div>
                {collections
                  .filter((name) => name.toLowerCase().includes(filter.toLowerCase()))
                  .map((name) => (
                    <MenuItem
                      key={name}
                      checked={memberships.includes(name)}
                      closeOnSelect={false}
                      onSelect={() =>
                        setMemberships((current) =>
                          current.includes(name)
                            ? current.filter((n) => n !== name)
                            : [...current, name],
                        )
                      }
                    >
                      {name}
                    </MenuItem>
                  ))}
              </div>
            ) : (
              <>
                <MenuItem
                  icon={<Play className="size-4" strokeWidth={1.75} />}
                  onSelect={() => toast({ message: 'Playing' })}
                >
                  Play
                </MenuItem>
                <MenuItem icon={<Info className="size-4" strokeWidth={1.75} />} disabled>
                  Details (disabled)
                </MenuItem>
                <MenuItem
                  trailing={<ChevronRight className="size-4" strokeWidth={1.75} />}
                  closeOnSelect={false}
                  onSelect={() => setView('collections')}
                >
                  Add to collection…
                </MenuItem>
                <MenuSeparator />
                <MenuItem
                  danger
                  icon={<Trash2 className="size-4" strokeWidth={1.75} />}
                  onSelect={() => toast({ message: 'Moved to trash' })}
                >
                  Move to trash
                </MenuItem>
              </>
            )
          }
        </Menu>
        <Menu
          aria-label="Sort"
          align="start"
          trigger={
            <>
              {speed}
              <ChevronDown aria-hidden className="size-4" strokeWidth={1.75} />
            </>
          }
          triggerClassName="bg-inset border-line-strong text-primary inline-flex h-10 cursor-pointer items-center gap-2 rounded-sm border px-3 text-base"
        >
          {['0.5×', '1×', '1.5×', '2×'].map((option) => (
            <MenuItem key={option} checked={speed === option} onSelect={() => setSpeed(option)}>
              {option}
            </MenuItem>
          ))}
        </Menu>
      </div>
    </DemoBlock>
  )
}

function SkeletonsDemo() {
  return (
    <DemoBlock label="Skeletons">
      <div className="grid max-w-md grid-cols-3 gap-4">
        <Skeleton className="aspect-[2/3]" />
        <Skeleton className="aspect-[2/3]" />
        <div className="flex flex-col gap-2">
          <Skeleton className="aspect-video" />
          <Skeleton className="h-4 w-full" />
          <Skeleton className="h-3 w-2/3" />
        </div>
      </div>
    </DemoBlock>
  )
}

function DialogAndToastDemo() {
  const [confirmOpen, setConfirmOpen] = useState(false)
  const { toast } = useToast()

  return (
    <DemoBlock label="Dialog & toast">
      <div className="flex flex-wrap gap-3">
        <Button variant="secondary" onClick={() => setConfirmOpen(true)}>
          <Trash2 aria-hidden className="size-4" strokeWidth={1.75} />
          Delete item…
        </Button>
        <Button
          variant="secondary"
          onClick={() =>
            toast({
              message: 'Moved to Trash',
              action: { label: 'Undo', onClick: () => toast({ message: 'Restored' }) },
            })
          }
        >
          Show undo toast
        </Button>
        <Button
          variant="ghost"
          onClick={() => toast({ message: 'Media B disconnected' })}
        >
          <Plus aria-hidden className="size-4" strokeWidth={1.75} />
          Queue plain toast
        </Button>
      </div>

      <Dialog
        open={confirmOpen}
        onClose={() => setConfirmOpen(false)}
        title="Move to trash?"
        footer={
          <>
            <Button variant="ghost" onClick={() => setConfirmOpen(false)}>
              Cancel
            </Button>
            <Button
              variant="danger"
              onClick={() => {
                setConfirmOpen(false)
              }}
            >
              Move to trash
            </Button>
          </>
        }
      >
        <p className="text-base text-secondary">
          "Big Buck Bunny" will be kept in Trash for 7 days, then removed from disk.
        </p>
      </Dialog>
    </DemoBlock>
  )
}
