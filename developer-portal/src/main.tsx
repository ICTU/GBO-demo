import React, { Suspense, lazy } from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import './styles/tokens.css'

// Two entry points, no router: the console itself and the bron-playground.
// GraphiQL + Voyager are several times the size of the console, so the
// playground is a lazy chunk that a reader who never opens /playground never
// downloads. nginx (try_files) and the vite dev-server both fall back to
// index.html, so the path resolves without server-side routing.
const BronPlayground = lazy(() => import('./playground/BronPlayground'))

const isPlayground = window.location.pathname.replace(/\/+$/, '') === '/playground'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    {isPlayground ? (
      // Fallback styled inline: the playground's own stylesheet travels with
      // the chunk this is waiting for.
      <Suspense fallback={<div style={{ padding: 24, color: 'var(--mute)' }}>Playground laden…</div>}>
        <BronPlayground />
      </Suspense>
    ) : (
      <App />
    )}
  </React.StrictMode>,
)
