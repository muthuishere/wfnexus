#!/usr/bin/env node
// Rebuild the architecture animation from run-flow.graph.json.
//
//   node site/diagram/build.mjs            # HTML canvas page only (needs `motion`)
//   node site/diagram/build.mjs --video    # also MP4 + GIF (needs playwright + ffmpeg)
//
// `motion canvas` (the diagram-motion skill) lays out and animates the graph; this script only
// recolours its accent to the wfnexus violet (apps/ui/src/index.css) and records a loop. The
// outputs are committed under site/public/, so the site build never needs motion or ffmpeg.
import { execFileSync } from 'node:child_process';
import { readFileSync, writeFileSync, mkdirSync, rmSync, readdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const pub = path.resolve(here, '../public');
const html = path.join(pub, 'run-flow.html');

execFileSync('motion', ['canvas', '--out', html, path.join(here, 'run-flow.graph.json')], { stdio: 'inherit' });

// motion's palette is warm-orange + indigo; swap to the wfnexus violet ramp.
const SWAP = {
	'#e08a1e': '#7c3aed', '74,92,208': '109,40,217', '#b9c0ea': '#c4b5fd', '#c3c9ee': '#ddd6fe',
	'#a07a3a': '#6d28d9', '#fbf4e8': '#f5f3ff', '#e0a955': '#a78bfa', '#e3b877': '#c4b5fd', '#dcc6a8': '#ddd6fe',
	'#7fd6a0': '#a78bfa', '#8fd0e8': '#c4b5fd',
};
let s = readFileSync(html, 'utf8');
for (const [a, b] of Object.entries(SWAP)) s = s.split(a).join(b);
// Hide the fps counter in the embedded copy.
s = s.replace('</head>', '<style>#fps,.fps{display:none!important}</style></head>');
writeFileSync(html, s);
console.log('recoloured', html);

if (process.argv.includes('--video')) {
	const { chromium } = await import('playwright');
	const tmp = path.join(here, '.rec');
	rmSync(tmp, { recursive: true, force: true });
	mkdirSync(tmp, { recursive: true });
	const b = await chromium.launch();
	const ctx = await b.newContext({ viewport: { width: 1600, height: 1000 }, recordVideo: { dir: tmp, size: { width: 1600, height: 1000 } } });
	const p = await ctx.newPage();
	await p.goto('file://' + html);
	await p.waitForTimeout(16500);
	await ctx.close();
	await b.close();
	const webm = path.join(tmp, readdirSync(tmp).find((f) => f.endsWith('.webm')));
	const mp4 = path.join(pub, 'run-flow.mp4');
	const gif = path.join(pub, 'run-flow.gif');
	// Drop the first 0.5 s (page load), keep a 15 s loop.
	execFileSync('ffmpeg', ['-y', '-loglevel', 'error', '-ss', '0.5', '-t', '15', '-i', webm, '-vf', 'scale=1280:-2', '-c:v', 'libx264', '-pix_fmt', 'yuv420p', '-movflags', '+faststart', '-an', mp4], { stdio: 'inherit' });
	execFileSync('ffmpeg', ['-y', '-loglevel', 'error', '-i', mp4, '-vf', 'fps=12,scale=800:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=64[p];[b][p]paletteuse', gif], { stdio: 'inherit' });
	rmSync(tmp, { recursive: true, force: true });
	console.log('wrote', mp4, gif);
}
