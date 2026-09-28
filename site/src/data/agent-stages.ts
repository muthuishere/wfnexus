// Which agent skill drives each stage of the journey. Whether a skill is "available" or
// "coming" is NOT written here: AgentStages.astro checks for skills/<skill>/SKILL.md at build
// time, so a skill that lands on main flips to available on the next deploy by itself.
export const stages = [
	{
		stage: 'Install',
		skill: 'wfnexus-setup',
		does: 'Brings wfx-server up (docker compose), installs the CLI and the skills, and checks the wiring with `wfx doctor`.',
		ask: 'set up wfnexus on this machine',
	},
	{
		stage: 'Build',
		skill: 'workflow-author',
		does: 'Turns the work you describe into a workflow: it interviews you, drafts the YAML, proves it with `wfx dryrun` and installs it with `wfx apply`.',
		ask: 'automate this as a wfnexus workflow',
	},
	{
		stage: 'Deploy',
		skill: 'wfnexus-deploy',
		does: 'Puts the workflow where it will run: `wfx apply` on a server, or `wfx publish` to a host or a git registry.',
		ask: 'deploy this workflow',
	},
	{
		stage: 'Run and approve',
		skill: 'approval-desk',
		does: 'Shows which runs are waiting on a human, and approves, rejects or answers them (`wfx approve`, `wfx reject`, `wfx answer`).',
		ask: 'what is waiting on me?',
	},
];
