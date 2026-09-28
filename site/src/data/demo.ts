// The demo video and its chapters: the ONE place to change when the video is re-recorded.
// Files live in site/public/ (so they are served at /wfnexus/<file>). `t` is seconds.
export const demo = {
	video: 'demo.mp4',
	poster: 'poster.jpg',
	eyebrow: 'wfnexus · one real Claude Code session · no voice, subtitles on',
	title: 'Install. Build. Deploy. Approve.',
	lede:
		"One real Claude Code session, about 22 minutes, cut to 10 by speeding up the waiting. Every stage is driven by an agent skill: wfnexus-setup installs the Docker stack on free models, workflow-author builds Priya's Friday release-notes workflow, wfnexus-deploy publishes and schedules it, and approval-desk puts every change in front of her. She rejects a draft, a fix goes through review, the agent finds a real bug in the skill's own example, and v1.0.2 ships.",
	chapters: [
		{ t: 5, label: '0:05', title: 'Install (wfnexus-setup)' },
		{ t: 96.5, label: '1:36', title: 'Build the workflow (workflow-author)' },
		{ t: 224.4, label: '3:44', title: 'Deploy to the team server (wfnexus-deploy)' },
		{ t: 310.3, label: '5:10', title: 'The approval gate (approval-desk)' },
		{ t: 392.8, label: '6:32', title: 'Reject, fix, re-ship' },
		{ t: 542.8, label: '9:02', title: 'Approved: the notes as committed' },
	],
};
