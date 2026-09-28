#!/usr/bin/env node
// Verify that every name the site states actually exists in the code on this commit.
// Adapted from toolnexus's site/scripts/verify-symbols.mjs: the same "name presence" idea,
// pointed at wfnexus's surfaces instead of seven language ports.
//
//   node site/scripts/verify-symbols.mjs           # report
//   node site/scripts/verify-symbols.mjs --strict  # exit 1 if anything is unresolved
//
// Two sources of claims:
//
//   1. src/data/claims.json — hand-listed claims the prose leans on: Go identifiers in the
//      package they are said to live in, YAML keys the schema is said to accept, repo paths,
//      and commits a "real result" cites.
//   2. A SCAN of every page under src/content/docs (hand-written and generated), which pulls out
//      and checks, with no manifest to forget to update:
//        - `wfx <verb> [<sub>]`    → must be a command in the usage text `wfx help` prints
//        - WFX_*                   → must be read somewhere in apps/api (non-test Go)
//        - internal/<pkg>          → must be a directory under apps/api/internal
//        - templates/<name>, skills/<name>, workflows/<name>.yaml → must exist
//        - /wfnexus/<asset>.<ext>  → must exist in site/public
//      plus the animation's graph (diagram/run-flow.graph.json) chips.
//
// Deliberately loose about syntax, strict about presence: a renamed command, a removed env var,
// a deleted template or a misspelt package fails here instead of shipping as a confident lie.

import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { wfxCommands, generate } from './generate-pages.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const siteRoot = path.resolve(here, '..');
const repoRoot = path.resolve(siteRoot, '..');
const docsRoot = path.join(siteRoot, 'src/content/docs');
const R = (p) => path.join(repoRoot, p);

// Generated pages are scanned too, so make sure they reflect this commit.
generate();

const unresolved = [];
let checked = 0;
const fail = (where, claim, why) => unresolved.push({ where, claim, why });

function grepGo(ident, dir) {
	try {
		const out = execFileSync('grep', ['-rlw', '--include=*.go', '--exclude=*_test.go', '--', ident, R(dir)], { encoding: 'utf8' });
		return out.trim() ? out.trim().split('\n').length : 0;
	} catch {
		return 0;
	}
}

// ---------------------------------------------------------------------------
// 1. the claims manifest
// ---------------------------------------------------------------------------

const claims = JSON.parse(fs.readFileSync(path.join(siteRoot, 'src/data/claims.json'), 'utf8'));

for (const { symbol, in: dir } of claims.go ?? []) {
	checked++;
	if (!fs.existsSync(R(dir))) fail('claims.json go', `${dir}.${symbol}`, 'package directory does not exist');
	else if (grepGo(symbol, dir) === 0) fail('claims.json go', `${dir}.${symbol}`, 'identifier not found in the package (non-test Go)');
}

const yamlTags = new Set();
for (const f of fs.readdirSync(R('apps/api/internal/workflow')).filter((f) => f.endsWith('.go') && !f.endsWith('_test.go'))) {
	for (const m of fs.readFileSync(R(`apps/api/internal/workflow/${f}`), 'utf8').matchAll(/yaml:"([a-z_-]+)/g)) yamlTags.add(m[1]);
}
for (const key of claims.yamlKeys ?? []) {
	checked++;
	if (!yamlTags.has(key)) fail('claims.json yamlKeys', key, 'no `yaml:"…"` tag with this name in internal/workflow');
}

for (const p of claims.paths ?? []) {
	checked++;
	if (!fs.existsSync(R(p))) fail('claims.json paths', p, 'path does not exist');
}

const results = JSON.parse(fs.readFileSync(path.join(siteRoot, 'src/data/template-results.json'), 'utf8'));
const commits = [...(claims.commits ?? []), ...Object.values(results.templates).flatMap((r) => r.commits ?? [])];
for (const [name, r] of Object.entries(results.templates)) {
	checked++;
	if (!fs.existsSync(R(`templates/${name}`)) && !fs.existsSync(R(`templates/${name}.yaml`))) fail('template-results.json', name, 'no such template');
	if (!r.commits?.length) fail('template-results.json', name, 'a result must cite at least one commit');
}
for (const c of commits) {
	checked++;
	try {
		execFileSync('git', ['-C', repoRoot, 'cat-file', '-e', `${c}^{commit}`], { stdio: 'ignore' });
	} catch {
		fail('commits', c, 'commit not found in this repository (a shallow clone? CI checks out with fetch-depth: 0)');
	}
}

// ---------------------------------------------------------------------------
// 2. scan every page
// ---------------------------------------------------------------------------

const commands = wfxCommands();
// Commands the binary handles but the usage text does not list (e.g. `wfx help` itself). Each
// must carry literal evidence found in apps/api/cmd/wfx/main.go, or it is not accepted.
const mainGo = fs.readFileSync(R('apps/api/cmd/wfx/main.go'), 'utf8');
for (const { command, evidence } of claims.extraCommands ?? []) {
	checked++;
	if (!mainGo.includes(evidence)) fail('claims.json extraCommands', command, `evidence ${JSON.stringify(evidence)} not found in cmd/wfx/main.go`);
	else commands.add(command), commands.add(command.split(' ')[0]);
}
const verbs = new Set([...commands].filter((c) => !c.includes(' ')));
const subs = new Map(); // verb -> set of known sub-words (only for verbs that HAVE subcommands in help)
for (const c of commands) {
	const [v, s] = c.split(' ');
	if (s) (subs.get(v) ?? subs.set(v, new Set()).get(v)).add(s);
}

const envVars = new Set();
for (const m of execFileSync('grep', ['-rhoE', '--include=*.go', '--exclude=*_test.go', 'WFX_[A-Z_]+', R('apps/api')], { encoding: 'utf8' }).matchAll(/WFX_[A-Z_]+/g)) envVars.add(m[0]);

const internalPkgs = new Set(fs.readdirSync(R('apps/api/internal')));
const publicFiles = new Set(fs.readdirSync(path.join(siteRoot, 'public')));

function walk(dir, out = []) {
	for (const n of fs.readdirSync(dir).sort()) {
		const f = path.join(dir, n);
		if (fs.statSync(f).isDirectory()) walk(f, out);
		else if (/\.mdx?$/.test(n)) out.push(f);
	}
	return out;
}
const scanFiles = [...walk(docsRoot), ...['src/data/demo.ts', 'src/data/agent-stages.ts'].map((f) => path.join(siteRoot, f))];

for (const file of scanFiles) {
	const rel = path.relative(siteRoot, file);
	const src = fs.readFileSync(file, 'utf8');

	// wfx commands: only inside code (inline `…` or fenced blocks), where a command is a claim.
	const codeBits = [...src.matchAll(/```[\s\S]*?```|`[^`\n]+`/g)].map((m) => m[0]);
	for (const bit of codeBits) {
		for (const m of bit.matchAll(/(?:^|[\s`(])wfx ([a-z][a-z-]*)(?: ([a-z][a-z-]*))?/gm)) {
			const [, v, s] = m;
			checked++;
			if (!verbs.has(v)) {
				fail(rel, `wfx ${v}`, 'not a command in `wfx help`');
				continue;
			}
			// A second bare word after a verb that has subcommands must be one of them.
			if (s && subs.has(v) && !subs.get(v).has(s) && !/^[a-z]+-[a-z-]+$/.test(s) && ['workflow', 'project', 'workers', 'state', 'env', 'context', 'install', 'registry', 'sources'].includes(v)) {
				fail(rel, `wfx ${v} ${s}`, `\`wfx ${v}\` has no \`${s}\` in \`wfx help\``);
			}
		}
	}

	for (const m of src.matchAll(/\bWFX_[A-Z_]+\b/g)) {
		checked++;
		if (!envVars.has(m[0])) fail(rel, m[0], 'env var not read anywhere in apps/api');
	}
	for (const m of src.matchAll(/\binternal\/([a-z]+)\b/g)) {
		checked++;
		if (!internalPkgs.has(m[1])) fail(rel, `internal/${m[1]}`, 'no such package under apps/api/internal');
	}
	for (const m of src.matchAll(/(?<![\w/.-])(templates|skills)\/([a-z][a-z0-9-]*[a-z0-9])(?=[/`\s)'"]|\.yaml)/g)) {
		const [, kind, name] = m;
		if (kind === 'templates' && name === 'bug-fixer') {
			checked++;
			if (!fs.existsSync(R('templates/bug-fixer.yaml'))) fail(rel, m[0], 'missing');
			continue;
		}
		checked++;
		if (!fs.existsSync(R(`${kind}/${name}`))) fail(rel, m[0], `no such directory ${kind}/${name}`);
	}
	for (const m of src.matchAll(/(?<![\w.\/-])workflows\/([a-z0-9-]+\.yaml)/g)) {
		checked++;
		if (!fs.existsSync(R(`workflows/${m[1]}`))) fail(rel, m[0], 'no such workflow file');
	}
	for (const m of src.matchAll(/\/wfnexus\/([\w.-]+\.(?:mp4|jpg|png|gif|svg|html|webm))/g)) {
		checked++;
		if (!publicFiles.has(m[1])) fail(rel, m[0], 'asset missing from site/public');
	}
}

// the animation's chips
const graph = JSON.parse(fs.readFileSync(path.join(siteRoot, 'diagram/run-flow.graph.json'), 'utf8'));
for (const chip of graph.chips ?? []) {
	checked++;
	const m = chip.match(/^internal\/([a-z]+)$/);
	if (m && !internalPkgs.has(m[1])) fail('diagram/run-flow.graph.json', chip, 'no such package');
}

// ---------------------------------------------------------------------------

console.log(`checked ${checked} claims (${claims.go?.length ?? 0} Go symbols, ${claims.yamlKeys?.length ?? 0} YAML keys, ${commits.length} commits, and a scan of ${scanFiles.length} pages)`);
console.log(`known: ${commands.size} wfx commands, ${envVars.size} WFX_* env vars, ${internalPkgs.size} internal packages`);
console.log(`unresolved: ${unresolved.length}`);
for (const u of unresolved) console.log(`  ${u.where.padEnd(44)} ${String(u.claim).padEnd(32)} ${u.why}`);
if (process.argv.includes('--strict') && unresolved.length) process.exit(1);
