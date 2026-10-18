import { Merge } from 'lucide-react'
import { useMergeShow } from '../../api/queries.ts'
import type { ShowDetail } from '../../api/types.ts'
import { mutationErrorMessage } from '../../lib/mutationFeedback.ts'
import { useToast } from '../../ui/index.ts'
import { ShowPickerDialog } from './ShowPickerDialog.tsx'

/** Merge this show into another of yours; resolves with the survivor. */
export function MergeShowDialog({
  show,
  open,
  onClose,
  onMerged,
}: {
  show: Pick<ShowDetail, 'id' | 'title' | 'episode_count'>
  open: boolean
  onClose: () => void
  onMerged: (into: ShowDetail) => void
}) {
  const merge = useMergeShow(show.id)
  const { toast } = useToast()
  const episodes = `${show.episode_count} ${show.episode_count === 1 ? 'episode' : 'episodes'}`
  return (
    <ShowPickerDialog
      open={open}
      onClose={onClose}
      title="Merge into another show"
      description={`${show.title}'s ${episodes} move to the show you pick, and ${show.title} is removed. New files with the same name go there too.`}
      excludeShowID={show.id}
      rankAgainst={show.title}
      confirmLabel="Merge"
      confirmIcon={<Merge aria-hidden className="size-5" strokeWidth={1.75} />}
      pending={merge.isPending}
      onConfirm={(into) =>
        merge.mutate(into.id, {
          onSuccess: onMerged,
          onError: (error) => toast({ message: mutationErrorMessage(error, "Couldn't merge the shows") }),
        })
      }
    />
  )
}
