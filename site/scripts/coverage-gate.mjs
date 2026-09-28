#!/usr/bin/env node
// The coverage gate, adapted from toolnexus's site/scripts/coverage-gate.mjs: an absence nobody
// would notice in review — a template with no page, a skill missing from the catalogue, a
// command missing from the reference, a sidebar link to nothing, an unfilled page — fails HERE.
//
//   node site/scripts/coverage-gate.mjs            # report, exit 0
//   node site/scripts/coverage-gate.mjs --strict   # exit 1 on any violation
//
// Checks are structural ("is it there, is it finished"), never stylistic.

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { generate, loadTemplates, loadSkills, wfxUsage, loadRegistry } from './generate-pages.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const siteRoot = path.resolve(here, '..');
const repoRoot = path.resolve(siteRoot, '..');
const docsRoot = path.join(siteRoot, 'src/content/docs');
const strict = process.argv.includes('--strict');

generate();

const violations = [];
const add = (kind, where, detail) => violations.push({ kind, where, detail });
const read = (rel) => fs.readFileSync(path.join(docsRoot, rel), 'utf8');
const exists = (rel) => fs.existsSync(path.join(docsRoot, rel));

// 1. every template has a page, with its steps; no page for a template that is gone
const tpls = loadTemplates();
const names = new Set(tpls.map((t) => t.wf.name));
for (const { wf, file } of tpls) {
	const rel = `templates/${wf.name}.mdx`;
	if (!exists(rel)) {
		add('template-no-page', wf.name, `${file} has no page`);
		continue;
	}
	const page = read(rel);
	for (const s of wf.steps ?? []) if (!page.includes('`' + s.id + '`')) add('template-step-missing', wf.name, `step ${s.id} is not on the page`);
	if (!read('templates/index.mdx').includes(`](./${wf.name}/)`)) add('template-not-indexed', wf.name, 'not listed on templates/index');
}
for (const f of fs.readdirSync(path.join(docsRoot, 'templates'))) {
	const n = f.replace(/\.mdx$/, '');
	if (n !== 'index' && !names.has(n)) add('template-orphan-page', f, 'no template in templates/ produces this page');
}
// the template dirs that hold no workflow.yaml are disclosed, not silently skipped
for (const d of fs.readdirSync(path.join(repoRoot, 'templates'))) {
	const full = path.join(repoRoot, 'templates', d);
	if (fs.statSync(full).isDirectory() && !fs.existsSync(path.join(full, 'workflow.yaml'))) {
		console.log(`note: templates/${d}/ has no workflow.yaml — not a template yet, so it has no page`);
	}
}

// 2. every skill is in the catalogue
const catalogue = read('skills/catalogue.mdx');
for (const s of loadSkills()) if (!catalogue.includes('`' + s.name + '`')) add('skill-missing', s.name, 'not in skills/catalogue');
for (const d of fs.readdirSync(path.join(repoRoot, 'skills'))) {
	if (fs.statSync(path.join(repoRoot, 'skills', d)).isDirectory() && !fs.existsSync(path.join(repoRoot, 'skills', d, 'SKILL.md'))) add('skill-no-manifest', d, 'skills/<dir> has no SKILL.md');
}

// 3. every line of `wfx help` is in the CLI reference
const cli = read('reference/cli.mdx');
for (const line of wfxUsage().split('\n')) {
	const m = line.match(/^\s+(wfx \S.*?)\s{2,}/);
	if (m && !cli.includes(m[1].trim())) add('cli-missing', m[1].trim(), 'usage line not in reference/cli');
}

// 4. every provider and classifier is on the registry page
const regPage = read('providers/registry.mdx');
const reg = loadRegistry();
for (const k of [...Object.keys(reg.providers ?? {}), ...Object.keys(reg.classifiers ?? {})]) if (!regPage.includes('`' + k + '`')) add('registry-missing', k, 'not on providers/registry');

// 5. every sidebar slug resolves to a page; every page is reachable from the sidebar
const cfg = fs.readFileSync(path.join(siteRoot, 'astro.config.mjs'), 'utf8');
const slugs = [...cfg.matchAll(/slug: '([^']+)'/g)].map((m) => m[1]);
const autodirs = [...cfg.matchAll(/autogenerate: \{ directory: '([^']+)' \}/g)].map((m) => m[1]);
const pageOf = (slug) => ['.mdx', '.md', '/index.mdx', '/index.md'].map((e) => slug + e).find(exists);
for (const s of slugs) if (!pageOf(s)) add('sidebar-dead-link', s, 'sidebar slug has no page');
function walk(dir, out = []) {
	for (const n of fs.readdirSync(dir)) {
		const f = path.join(dir, n);
		if (fs.statSync(f).isDirectory()) walk(f, out);
		else if (/\.mdx?$/.test(n)) out.push(path.relative(docsRoot, f));
	}
	return out;
}
for (const rel of walk(docsRoot)) {
	const slug = rel.replace(/\.mdx?$/, '').replace(/\/index$/, '');
	if (!slugs.includes(slug) && !autodirs.some((d) => slug === d || slug.startsWith(d + '/'))) add('page-orphan', rel, 'not reachable from the sidebar');
	// 6. no unfinished pages
	const src = read(rel);
	if (/\{\/\*\s*TODO|\bTODO:|\bTBD\b|lorem ipsum/i.test(src)) add('unfilled', rel, 'carries a TODO/TBD marker');
}

// 7. the demo: the video and poster the data file names exist, and chapters are in order
const demoSrc = fs.readFileSync(path.join(siteRoot, 'src/data/demo.ts'), 'utf8');
for (const k of ['video', 'poster']) {
	const f = demoSrc.match(new RegExp(`${k}: '([^']+)'`))?.[1];
	if (!f || !fs.existsSync(path.join(siteRoot, 'public', f))) add('demo-asset', k, `${f ?? '(none)'} missing from public/`);
}
const ts = [...demoSrc.matchAll(/\{ t: ([\d.]+)/g)].map((m) => +m[1]);
if (!ts.length || ts.some((t, i) => i && t <= ts[i - 1])) add('demo-chapters', 'demo.ts', 'chapters missing or not in ascending order');

// 8. the architecture animation's three forms exist
for (const f of ['run-flow.svg', 'run-flow.html', 'run-flow.mp4', 'run-flow.gif']) if (!fs.existsSync(path.join(siteRoot, 'public', f))) add('animation-missing', f, 'missing from public/');

// ---------------------------------------------------------------------------
console.log(`checked ${tpls.length} templates, ${loadSkills().length} skills, the CLI usage, ${Object.keys(reg.providers ?? {}).length} providers, ${slugs.length} sidebar slugs, ${walk(docsRoot).length} pages`);
if (!violations.length) console.log('coverage gate: clean');
else {
	console.log(`coverage gate FAILED: ${violations.length} violation(s)`);
	for (const v of violations) console.log(`  ${v.kind.padEnd(22)} ${String(v.where).padEnd(34)} ${v.detail}`);
	if (strict) process.exit(1);
}
