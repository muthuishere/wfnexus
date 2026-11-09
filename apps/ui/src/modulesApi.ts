// The Modules page's calls. Same fetch shape as api.ts (JSON in/out, the
// server's `error` field as the message, cookie auth), kept separate so the
// Modules page owns its own types.

export type CLIStatus = { name: string; installed: boolean; version: string; install?: string }
export type ModuleService = {
  key: string; module: string; account: string; profile?: string; state: string; owner?: string; restarts: number; lastError?: string
  lastHeartbeat?: string; serviceHeartbeat?: string; watches: unknown[]
}
export type Module = {
  name: string; version?: string; description?: string; source?: string; dir: string
  requires: { env?: string[]; secrets?: string[]; cli?: { name: string; check: string; install?: string }[] }
  commands?: Record<string, { description?: string; inputs?: Record<string, { description?: string }> }>
  services?: Record<string, { description?: string; command?: string }>
  missing: string[]; ready: boolean; instances: ModuleService[]; clis: CLIStatus[]
}
export type Profile = { name: string; account?: string; default?: boolean; inputs?: Record<string, string> }
export type WfxInfo = {
  version: { version: string; commit?: string; date?: string }
  latest: string; defaultProvider: string; defaultChannel: string
  channels: { name: string; kind: string; default: boolean; inbound: boolean }[]
  upgrade: string
}

async function j<T>(r: Promise<Response>): Promise<T> {
  const res = await r
  if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || res.statusText)
  return res.json()
}
const send = (method: string, url: string, body?: unknown) =>
  fetch(url, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
const m = (name: string) => `/api/modules/${encodeURIComponent(name)}`

/** A built-in tool (aos, ctx-optimize, jevx): pinned floor, the version every
 *  runner should run, the latest release, and what each runner reports. */
export type BuiltinTool = { name: string; repo: string; pinned: string; desired: string; latest?: string; runners: Record<string, string> }

export const modulesApi = {
  builtins: (latest = false) => j<{ tools: BuiltinTool[] }>(fetch('/api/builtins' + (latest ? '?latest=1' : ''))),
  updateBuiltin: (name: string, version = '') => j<{ name: string; version: string }>(send('POST', `/api/builtins/${encodeURIComponent(name)}/update`, { version })),
  list: () => j<{ modules: Module[]; errors: string[] }>(fetch('/api/modules')),
  get: (name: string) => j<Module>(fetch(m(name))),
  profiles: (name: string) => j<{ profiles: Profile[] | null }>(fetch(m(name) + '/profiles')),
  install: (name: string, cli: string) => j<{ ok: boolean; log: string; error?: string }>(send('POST', m(name) + '/install', { cli })),
  configure: (name: string, key: string, value: string) => j<{ ok: boolean }>(send('PUT', m(name) + '/config', { name: key, value })),
  wfx: () => j<WfxInfo>(fetch('/api/doctor/wfx')),
}
