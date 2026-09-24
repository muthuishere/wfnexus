// Boots the built wfx-server in an isolated sandbox for the e2e suite.
//
// It deliberately uses the SHIPPED binary rather than `go run`: the thing under
// test is what a person downloads, embedded UI and all. If the binary is not
// built, it says so instead of silently testing a stale one.
import { spawn } from 'node:child_process'
import { mkdtempSync, writeFileSync, mkdirSync, existsSync, cpSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const repo = resolve(here, '../..')
const bin = join(repo, 'bin', 'wfx-server')
const port = process.argv[2] || '8433'

if (!existsSync(bin)) {
  console.error(`missing ${bin} — run \`task build:api\` first (the suite tests the built binary, not the source)`)
  process.exit(1)
}

// Its own HOME and workflows dir: a test must never write to the developer's
// real ~/.local/share/wfnexus, and must never depend on what is in it.
const sandbox = mkdtempSync(join(tmpdir(), 'wfx-e2e-'))
const home = join(sandbox, 'home')
const workflows = join(sandbox, 'workflows')
mkdirSync(home, { recursive: true })
mkdirSync(workflows, { recursive: true })
cpSync(join(here, 'fixtures', 'workflows'), workflows, { recursive: true })

const config = join(sandbox, 'config.yaml')
writeFileSync(config, `mode: local\naddr: "127.0.0.1:${port}"\npaths:\n  workflows: ${workflows}\n`)

const child = spawn(bin, [], {
  cwd: sandbox,
  env: { ...process.env, HOME: home, WFX_CONFIG: config, WFX_SKILLS_DIR: join(sandbox, 'no-skills') },
  stdio: 'inherit',
})
child.on('exit', c => process.exit(c ?? 0))
for (const sig of ['SIGINT', 'SIGTERM']) process.on(sig, () => child.kill(sig))
