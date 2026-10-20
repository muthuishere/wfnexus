// Boots the built wfx-server in an isolated sandbox for the e2e suite.
//
// It deliberately uses the SHIPPED binary rather than `go run`: the thing under
// test is what a person downloads, embedded UI and all. If the binary is not
// built, it says so instead of silently testing a stale one.
import { spawn } from 'node:child_process'
import { mkdtempSync, writeFileSync, mkdirSync, existsSync, cpSync, openSync, readSync, closeSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const repo = resolve(here, '../..')
const bin = process.env.WFX_SERVER_BIN || join(repo, 'bin', 'wfx-server')
const port = process.argv[2] || '8433'

if (!existsSync(bin)) {
  console.error(`missing ${bin} — run \`task build:api\` first (the suite tests the built binary, not the source)`)
  process.exit(1)
}

// bin/ is shared with cross-builds (a linux binary for the server lands at the
// same path), and spawning one on a Mac fails as a bare `spawn ENOEXEC`. Read
// the header and say what is there instead.
const wrong = foreignBinary(bin)
if (wrong) {
  console.error(`${bin} is a ${wrong} binary, not one for this ${process.platform}/${process.arch} machine — ` +
    'run `task build:api` to build a host binary, or point WFX_SERVER_BIN at one')
  process.exit(1)
}

/** null when the executable can run here, else what it is built for. */
function foreignBinary(path) {
  const b = Buffer.alloc(20)
  const fd = openSync(path, 'r')
  try { readSync(fd, b, 0, 20, 0) } finally { closeSync(fd) }
  const arch = { 0x3e: 'x64', 0xb7: 'arm64', 0x01000007: 'x64', 0x0100000c: 'arm64', 0x8664: 'x64', 0xaa64: 'arm64' }
  let os, cpu
  if (b.readUInt32BE(0) === 0x7f454c46) { os = 'linux'; cpu = arch[b.readUInt16LE(18)] }
  else if (b.readUInt32LE(0) === 0xfeedfacf) { os = 'darwin'; cpu = arch[b.readUInt32LE(4)] }
  else if (b.toString('latin1', 0, 2) === 'MZ') { os = 'win32' } // arch lives deeper in a PE; the OS is the usual mismatch
  else return null // a script or something unknown: let spawn speak
  if (os !== process.platform || (cpu && cpu !== process.arch)) return `${os}${cpu ? '/' + cpu : ''}`
  return null
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
