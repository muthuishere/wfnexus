// Every address in the app is a place in project → workflow → run, so the
// links are built in one spot and read that way.
//
// `+new` cannot collide with a workflow: a workflow name is lowercase letters,
// digits and hyphens (workflow/save.go), so a `+` is never one.
const e = encodeURIComponent

export const href = {
  projects: () => '#/projects',
  project: (p: string) => `#/projects/${e(p)}`,
  newWorkflow: (p: string) => `#/projects/${e(p)}/+new`,
  fromTemplate: (p: string) => `#/projects/${e(p)}/+template`,
  workflow: (p: string, w: string) => `#/projects/${e(p)}/${e(w)}`,
  edit: (p: string, w: string) => `#/projects/${e(p)}/${e(w)}/edit`,
  startRun: (p: string, w: string) => `#/projects/${e(p)}/${e(w)}/run`,
  run: (p: string, w: string, id: string) => `#/projects/${e(p)}/${e(w)}/runs/${id}`,
}

/** The short name of a workflow that may be spelled `project/name`. */
export function shortName(w: string): string { return w.includes('/') ? w.slice(w.indexOf('/') + 1) : w }
