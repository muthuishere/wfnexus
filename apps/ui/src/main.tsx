import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
// Self-hosted faces, bundled by Vite into dist/ and so into the binary: no CDN,
// works offline and on an air-gapped server. Upright weight axis only; the UI
// never sets italic body text and the italic files would double the payload.
import '@fontsource-variable/inter/wght.css'
import '@fontsource-variable/jetbrains-mono/wght.css'
import './index.css'
import App from './App'

createRoot(document.getElementById('root')!).render(<StrictMode><App /></StrictMode>)
