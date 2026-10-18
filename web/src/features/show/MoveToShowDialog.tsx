import { usePatchItem } from '../../api/queries.ts'
import type { ItemDetail, ShowRef } from '../../api/types.ts'
import { mutationErrorMessage } from '../../lib/mutationFeedback.ts'
import { useToast } from '../../ui/index.ts'
import { ShowPickerDialog } from './ShowPickerDialog.tsx'

/** File an item under another show. The move teaches the matcher, so later
    files with the same parsed name follow it. */
export function MoveToShowDialog({
  item,
  open,
  onClose,
  onMoved,
}: {
  item: { id: number; title: string; show: ShowRef | null }
  open: boolean
  onClose: () => void
  onMoved?: (item: ItemDetail) => void
}) {
  const patch = usePatchItem(item.id)
  const { toast } = useToast()
  return (
    <ShowPickerDialog
      open={open}
      onClose={onClose}
      title="Move to show"
      description={`${item.title} moves to the show you pick. New files with the same name go there too.`}
      excludeShowID={item.show?.id}
      rankAgainst={item.show?.title ?? item.title}
      confirmLabel="Move"
      pending={patch.isPending}
      onConfirm={(show) =>
        patch.mutate(
          { show_id: show.id },
          {
            onSuccess: (updated) => {
              onClose()
              onMoved?.(updated)
            },
            onError: (error) => toast({ message: mutationErrorMessage(error, "Couldn't move the episode") }),
          },
        )
      }
    />
  )
}
