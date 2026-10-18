import { Plus } from 'lucide-react'
import type { Collection } from '../../api/types.ts'
import { Input, MenuItem } from '../../ui/index.ts'

/** Filterable collection list with a create row, for a Menu body: the
    library's selection bar and the show page's More menu. */
export function CollectionListPicker({
  collections,
  filter,
  setFilter,
  isChecked,
  onPick,
  onCreate,
  keepOpenOnPick = false,
}: {
  collections: Collection[]
  filter: string
  setFilter: (value: string) => void
  isChecked?: (collection: Collection) => boolean
  onPick: (collection: Collection) => void
  onCreate: (name: string) => void
  keepOpenOnPick?: boolean
}) {
  const trimmed = filter.trim()
  const matches = collections.filter((collection) =>
    collection.name.toLowerCase().includes(trimmed.toLowerCase()),
  )
  const canCreate =
    trimmed.length > 0 &&
    !collections.some((collection) => collection.name.toLowerCase() === trimmed.toLowerCase())

  return (
    <div className="min-w-56">
      <div className="p-1">
        <Input
          aria-label="Filter collections"
          placeholder="Filter"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
        />
      </div>
      <div className="max-h-56 overflow-y-auto">
        {matches.map((collection) => (
          <MenuItem
            key={collection.id}
            checked={isChecked?.(collection)}
            closeOnSelect={!keepOpenOnPick}
            onSelect={() => onPick(collection)}
          >
            {collection.name}
          </MenuItem>
        ))}
        {matches.length === 0 && !canCreate && (
          <p className="text-tertiary px-3 py-2 text-sm">No collections</p>
        )}
      </div>
      {canCreate && (
        <MenuItem
          icon={<Plus className="size-4" strokeWidth={1.75} />}
          closeOnSelect={!keepOpenOnPick}
          onSelect={() => {
            onCreate(trimmed)
            setFilter('')
          }}
        >
          New collection "{trimmed}"
        </MenuItem>
      )}
    </div>
  )
}
