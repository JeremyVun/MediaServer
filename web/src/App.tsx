import { Route, Routes } from 'react-router'
import { LibraryPage } from './features/library/LibraryPage.tsx'
import { ItemDetailPage } from './features/item-detail/ItemDetailPage.tsx'
import { SettingsPage } from './features/settings/SettingsPage.tsx'
import { CollectionsPage } from './features/collections/CollectionsPage.tsx'
import { PlayerPage } from './player/Player.tsx'
import { DemoPage } from './features/demo/DemoPage.tsx'

export default function App() {
  return (
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
  )
}
