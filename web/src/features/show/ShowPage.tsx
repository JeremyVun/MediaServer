import { Link, useParams } from 'react-router'
import { ArrowLeft } from 'lucide-react'
import { useShow } from '../../api/queries.ts'

// Placeholder until phase 6b builds the show page.
export function ShowPage() {
  const id = Number(useParams().id)
  const show = useShow(Number.isFinite(id) ? id : null)
  return (
    <main className="mx-auto max-w-6xl px-4 py-6 sm:px-8">
      <Link to="/" className="text-secondary hover:text-primary inline-flex h-11 items-center gap-2 text-sm">
        <ArrowLeft aria-hidden className="size-4" strokeWidth={1.75} />
        Library
      </Link>
      <h1 className="mt-4 text-2xl font-semibold">{show.data?.title ?? ''}</h1>
    </main>
  )
}
