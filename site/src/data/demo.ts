// The demo video and its chapters: the ONE place to change when the video is re-recorded.
// Files live in site/public/ (so they are served at /wfnexus/<file>). `t` is seconds.
export const demo = {
	video: 'demo.mp4',
	poster: 'poster.jpg',
	eyebrow: 'wfnexus · real session · no voice, subtitles on',
	title: 'Describe it. Claude builds it. You approve it.',
	lede:
		"A real Claude Code session, unedited except for speed. Priya wants her Friday release-notes chore turned into a workflow. Claude builds a five-step wfnexus workflow, runs it on a free model, stops at a human approval gate, and publishes it to the team's git registry.",
	chapters: [
		{ t: 7.2, label: '0:07', title: 'The ask' },
		{ t: 27.2, label: '0:27', title: 'Claude builds the workflow' },
		{ t: 84.6, label: '1:24', title: 'Real problems, fixed live' },
		{ t: 126.8, label: '2:06', title: 'The run' },
		{ t: 159, label: '2:39', title: 'The approval gate' },
		{ t: 224.2, label: '3:44', title: 'Written, committed, published' },
	],
};
