import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ThemeProvider } from './theme/ThemeProvider.tsx'
import { ToastProvider } from './ui/index.ts'
import { SSEProvider } from './api/sse.tsx'
import { UploadProvider } from './features/upload/UploadProvider.tsx'
import App from './App.tsx'
import './theme/tokens.css'
// Discovers and bundles custom themes from src/theme/themes/*.theme.css.
import './theme/registry.ts'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      retry: 1,
    },
  },
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <ToastProvider>
          <SSEProvider>
            <BrowserRouter>
              <UploadProvider>
                <App />
              </UploadProvider>
            </BrowserRouter>
          </SSEProvider>
        </ToastProvider>
      </ThemeProvider>
    </QueryClientProvider>
  </StrictMode>,
)
