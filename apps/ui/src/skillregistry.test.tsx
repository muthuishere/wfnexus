import { describe, test, expect, beforeAll, afterEach } from 'vitest'
import { render, screen, cleanup, waitFor, within, fireEvent } from '@testing-library/react'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import App from './App'
import { api } from './api'

// The Skill registry page against the real server and a real git repository:
// a bare repo in a temp dir stands in for GitHub, so import, sync, change ref
// and remove all run the same git the server runs in production.
const thrown: unknown[] = []
beforeAll(() => {
  window.addEventListener('error', e => thrown.push(e.error ?? e.message))
  window.addEventListener('unhandledrejection', e => thrown.push(e.reason))
})
afterEach(() => { cleanup(); expect(thrown).toEqual([]) })

const gitEnv = { ...process.env, GIT_AUTHOR_NAME: 't', GIT_AUTHOR_EMAIL: 't@t', GIT_COMMITTER_NAME: 't', GIT_COMMITTER_EMAIL: 't@t' }
const git = (cwd: string, ...args: string[]) => execFileSync('git', args, { cwd, env: gitEnv }).toString().trim()

function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'wfx-skillsrc-'))
  const bare = join(root, 'ui-skills.git'), work = join(root, 'work')
  git(root, 'init', '-q', '--bare', '-b', 'main', bare)
  git(root, 'init', '-q', '-b', 'main', work)
  git(work, 'remote', 'add', 'origin', bare)
  const skill = (name: string, desc: string) => {
    mkdirSync(join(work, 'skills', name), { recursive: true })
    writeFileSync(join(work, 'skills', name, 'SKILL.md'), `---\nname: ${name}\ndescription: ${desc}\n---\n# ${name} body\n`)
  }
  const commit = (m: string) => { git(work, 'add', '-A'); git(work, 'commit', '-q', '-m', m); git(work, 'push', '-q', 'origin', 'HEAD') }
  return { bare, work, skill, commit }
}

function go(hash: string) {
  window.location.hash = hash
  return render(<App />)
}

const result = () => screen.findByLabelText('Result')

describe('the Skill registry page', () => {
  test('is in the sidebar next to Skills & tools', async () => {
    go('#/registry')
    const nav = document.querySelector('.side nav') as HTMLElement
    expect(within(nav).getByText('Skill registry').closest('a')?.getAttribute('href')).toBe('#/registry')
    await screen.findByText(/No skill sources yet|Skill sources/)
  })

  test('imports, syncs, changes ref, shows SKILL.md and removes a source', async () => {
    const f = fixture()
    f.skill('ui-alpha', 'first skill')
    f.skill('ui-beta', 'second skill')
    f.commit('one')
    git(f.work, 'tag', 'v1')
    git(f.work, 'push', '-q', 'origin', 'v1')

    // Import through the form.
    go('#/registry')
    fireEvent.click(await screen.findByText('Import from GitHub'))
    const form = screen.getByLabelText('Import skills')
    fireEvent.change(within(form).getByLabelText('Repository URL'), { target: { value: f.bare } })
    fireEvent.change(within(form).getByLabelText('Branch, tag or commit'), { target: { value: 'main' } })
    expect((within(form).getByLabelText('Path') as HTMLInputElement).value).toBe('skills')
    fireEvent.click(within(form).getByText('Import'))
    expect((await result()).textContent).toMatch(/imported 2 skill\(s\).*branch main/)
    const table = await screen.findByLabelText('Skill sources')
    const row = (await within(table).findByText('ui-skills')).closest('tr') as HTMLElement
    expect(row.textContent).toContain('branch')
    expect(row.textContent).toContain('2')

    // Sync picks up a new commit on the branch.
    f.skill('ui-gamma', 'third skill')
    f.commit('two')
    fireEvent.click(within(row).getByText('Sync'))
    await waitFor(async () => expect((await result()).textContent).toContain('+ ui-gamma'))

    // Change ref to a tag, picked from the remote's list.
    fireEvent.click(within(await rowOf('ui-skills')).getByText('Change ref'))
    const picker = await screen.findByLabelText('Change ref of ui-skills')
    await within(picker).findByRole('option', { name: 'v1' })
    fireEvent.change(within(picker).getByLabelText('Branch or tag'), { target: { value: 'v1' } })
    fireEvent.click(within(picker).getByText('Switch'))
    await waitFor(async () => {
      const t = (await result()).textContent || ''
      expect(t).toContain('tag v1')
      expect(t).toContain('− ui-gamma')
    })
    // A tag is a pin: Sync says so and changes nothing.
    fireEvent.click(within(await rowOf('ui-skills')).getByText('Sync'))
    await waitFor(async () => expect((await result()).textContent).toMatch(/pinned to tag v1/))

    // Clicking a source lists its skills and shows SKILL.md read-only.
    cleanup()
    go('#/registry/ui-skills')
    const card = await screen.findByLabelText('Skills in ui-skills')
    await within(card).findByText('ui-alpha')
    fireEvent.click(within(card).getAllByText('SKILL.md')[0])
    expect((await within(card).findByLabelText('ui-alpha SKILL.md')).textContent).toContain('# ui-alpha body')

    // Remove, after a confirmation.
    const realConfirm = window.confirm
    let asked = ''
    window.confirm = (m?: string) => { asked = m || ''; return true }
    try {
      fireEvent.click(within(await rowOf('ui-skills')).getByText('Remove'))
      await waitFor(async () => expect((await result()).textContent).toMatch(/removed ui-skills: 2 skill\(s\) unloaded/))
    } finally { window.confirm = realConfirm }
    expect(asked).toContain('Remove ui-skills?')
    expect((await api.skillSources()).find(s => s.name === 'ui-skills')).toBeUndefined()
  })
})

async function rowOf(name: string) {
  const table = await screen.findByLabelText('Skill sources')
  return (await within(table).findByText(name)).closest('tr') as HTMLElement
}
