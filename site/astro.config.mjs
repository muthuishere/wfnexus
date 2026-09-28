// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLlmsTxt from 'starlight-llms-txt';

// Project GitHub Pages: https://muthuishere.github.io/wfnexus
// https://astro.build/config
export default defineConfig({
	site: 'https://muthuishere.github.io',
	base: '/wfnexus',
	integrations: [
		starlight({
			title: 'wfnexus',
			// Remove the right-hand "On this page" table of contents site-wide.
			tableOfContents: false,
			description:
				'A developer-friendly workflow platform for coding agents: agent-skill steps with typed output, graph ordering, state between runs, human gates, and git as the record.',
			favicon: '/favicon.svg',
			plugins: [
				starlightLlmsTxt({
					projectName: 'wfnexus',
					description:
						'Typed pipelines for coding agents. A workflow is YAML; a step is a prompt + scoped skills/tools + a JSON-schema output contract, or a shell node. One Go binary (wfx-server) plus the wfx CLI.',
					details:
						'Steps run on http models, cli agents or acp agents (devin, opencode, codex). Calibrated classifiers (decide:, wfx judge, TypeSafe jev) sort verdicts into no/uncertain/yes. Human gates: approve, reject, answer. Workflow changes in git projects become branch + PR proposals.',
				}),
			],
			customCss: ['@fontsource-variable/inter', './src/styles/deemwar.css'],
			social: [
				{
					icon: 'github',
					label: 'GitHub',
					href: 'https://github.com/muthuishere/wfnexus',
				},
			],
			sidebar: [
				{
					label: 'Start here',
					items: [
						{ label: 'Overview', slug: 'index' },
						{ label: 'Quickstart', slug: 'quickstart' },
						{ label: 'Get started with an agent', slug: 'agent-start' },
						{ label: 'Demo video', slug: 'demo' },
					],
				},
				{
					label: 'Concepts',
					items: [
						{ label: 'Workflows and steps', slug: 'concepts/workflows-and-steps' },
						{ label: 'Projects, workflows, runs', slug: 'concepts/projects-and-runs' },
						{ label: 'Classifiers', slug: 'concepts/classifiers' },
						{ label: 'Human gates', slug: 'concepts/human-gates' },
						{ label: 'State and memory', slug: 'concepts/state-and-memory' },
						{ label: 'Env store and secrets', slug: 'concepts/env-and-secrets' },
						{ label: 'Workers', slug: 'concepts/workers' },
					],
				},
				{
					label: 'Git-native workflows',
					items: [{ label: 'Proposals and drift', slug: 'git-native/proposals' }],
				},
				{
					label: 'Providers and models',
					items: [
						{ label: 'Providers, ACP, free models', slug: 'providers' },
						{ label: 'Provider registry', slug: 'providers/registry' },
					],
				},
				{
					label: 'Templates',
					collapsed: false,
					items: [{ autogenerate: { directory: 'templates' } }],
				},
				{
					label: 'Agent skills',
					items: [
						{ label: 'How agents use wfx', slug: 'skills' },
						{ label: 'Skill catalogue', slug: 'skills/catalogue' },
						{ label: 'Import skills from git', slug: 'skills/import-from-git' },
					],
				},
				{
					label: 'Reference',
					items: [
						{ label: 'CLI reference', slug: 'reference/cli' },
						{ label: 'Architecture', slug: 'architecture' },
					],
				},
			],
		}),
	],
});
