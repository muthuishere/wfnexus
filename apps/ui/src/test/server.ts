import { spawn, type ChildProcess } from 'node:child_process'

// Boots the built wfx-server for the whole test file set, and takes it down
// after. The harness itself lives in tests/e2e/server.mjs so it stays usable
// from outside vitest.
let child: ChildProcess | undefined
const PORT = Number(process.env.WFX_E2E_PORT || 8433)

export async function setup() {
  child = spawn(process.execPath, ['../../tests/e2e/server.mjs', String(PORT)], {
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  let log = ''
  child.stdout?.on('data', d => { log += d })
  child.stderr?.on('data', d => { log += d })
  child.on('exit', code => { if (code) console.error(`wfx-server exited ${code}\n${log}`) })

  const deadline = Date.now() + 30_000
  for (;;) {
    try {
      const r = await fetch(`http://127.0.0.1:${PORT}/api/health`)
      if (r.ok) return
    } catch { /* not up yet */ }
    if (Date.now() > deadline) throw new Error(`wfx-server did not start in 30s:\n${log}`)
    await new Promise(r => setTimeout(r, 200))
  }
}

export async function teardown() {
  child?.kill('SIGTERM')
}
