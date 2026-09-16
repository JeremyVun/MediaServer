import { Suspense, lazy, useEffect } from 'react'
import { Route, Routes } from 'react-router'
import { LibraryPage } from './features/library/LibraryPage.tsx'

// Every route but the library is its own chunk so the first paint ships only
// the home screen. The rest are fetched a moment later in the background, so
// navigating after that is instant and the Suspense fallback rarely shows.
const loadItemDetail = () => import('./features/item-detail/ItemDetailPage.tsx')
const loadPlayer = () => import('./player/Player.tsx')
const loadCollections = () => import('./features/collections/CollectionsPage.tsx')
const loadSettings = () => import('./features/settings/SettingsPage.tsx')
const loadDemo = () => import('./features/demo/DemoPage.tsx')

const ItemDetailPage = lazy(() => loadItemDetail().then((m) => ({ default: m.ItemDetailPage })))
const PlayerPage = lazy(() => loadPlayer().then((m) => ({ default: m.PlayerPage })))
const CollectionsPage = lazy(() => loadCollections().then((m) => ({ default: m.CollectionsPage })))
const SettingsPage = lazy(() => loadSettings().then((m) => ({ default: m.SettingsPage })))
const DemoPage = lazy(() => loadDemo().then((m) => ({ default: m.DemoPage })))

const PRELOAD_DELAY_MS = 2_000

export default function App() {
  useEffect(() => {
    const timer = window.setTimeout(() => {
      for (const load of [loadPlayer, loadItemDetail, loadCollections, loadSettings]) {
        void load().catch(() => undefined)
      }
    }, PRELOAD_DELAY_MS)
    return () => window.clearTimeout(timer)
  }, [])

  return (
    <Suspense fallback={null}>
      <Routes>
        <Route path="/" element={<LibraryPage />} />
        <Route path="/items/:id" element={<ItemDetailPage />} />
        <Route path="/watch/:id" element={<PlayerPage />} />
        <Route path="/collections" element={<CollectionsPage />} />
        <Route path="/collections/:id" element={<CollectionsPage />} />
        <Route path="/settings" element={<SettingsPage />} />
        {/* Storybook-less demo of the ui/ primitives in both themes. */}
        <Route path="/demo" element={<DemoPage />} />
      </Routes>
    </Suspense>
  )
}
