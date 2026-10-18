import { useState, type FormEvent } from 'react'
import { usePatchShow } from '../../api/queries.ts'
import type { ShowDetail } from '../../api/types.ts'
import { mutationErrorMessage } from '../../lib/mutationFeedback.ts'
import { Button, Dialog, Input, useToast } from '../../ui/index.ts'

/** A renamed show keeps its name: TVmaze refreshes never change it again. */
export function RenameShowDialog(props: { show: ShowDetail; open: boolean; onClose: () => void }) {
  return <Rename key={String(props.open)} {...props} />
}

function Rename({ show, open, onClose }: { show: ShowDetail; open: boolean; onClose: () => void }) {
  const patch = usePatchShow(show.id)
  const { toast } = useToast()
  const [title, setTitle] = useState(show.title)
  const trimmed = title.trim()

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (!trimmed) return
    if (trimmed === show.title) return onClose()
    patch.mutate(
      { title: trimmed },
      {
        onSuccess: onClose,
        onError: (error) => toast({ message: mutationErrorMessage(error, "Couldn't rename the show") }),
      },
    )
  }

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="Rename show"
      footer={
        <>
          <Button variant="ghost" touch onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="rename-show" variant="primary" touch disabled={!trimmed} pending={patch.isPending}>
            Save
          </Button>
        </>
      }
    >
      <form id="rename-show" onSubmit={submit}>
        <Input aria-label="Show name" value={title} onChange={(e) => setTitle(e.target.value)} autoFocus />
      </form>
    </Dialog>
  )
}
