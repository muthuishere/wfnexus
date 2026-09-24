import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// The UI's own components, mounted in jsdom against a REAL wfx-server.
//
// Not a browser: this is our app, we ship one bundle, and cross-browser
// rendering is not the risk. The risk is the API handing the UI a shape it did
// not expect — which is exactly what jsdom catches, because the fetches are
// real. globalSetup boots the built binary on sqlite in a temp HOME, so a test
// run touches no postgres, no minio, no model and no developer data.
const PORT = Number(process.env.WFX_E2E_PORT || 8433)

export default defineConfig({
  plugins: [react()],
  test: {
    include: ['src/**/*.test.tsx'],
    environment: 'jsdom',
    // Relative fetches ('/api/...') resolve against this origin, so the
    // components talk to the real server without knowing they are in a test.
    environmentOptions: { jsdom: { url: `http://127.0.0.1:${PORT}/` } },
    globalSetup: ['./src/test/server.ts'],
    setupFiles: ['./src/test/setup.ts'],
    testTimeout: 30_000,
    hookTimeout: 60_000,
  },
})
