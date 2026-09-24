package skills

// Platform tools are tools a step can name that reach back into THIS PLATFORM
// rather than into the machine: read the catalogues, validate a workflow, dry
// run one.
//
// They exist so that authoring a workflow can itself be a workflow. An agent
// asked to write one needs the same three things a person needs — what names
// are available here, whether the draft is valid, and whether it would actually
// run — and giving it those as tools means it checks its own work in the loop
// rather than handing over something plausible.
//
// They are declared alongside the toolnexus built-ins, not in a separate
// vocabulary, because a step already says `tools: [read, bash]` and a second
// list to learn would be worse than a longer one. ADR 0011's rule applies: one
// registry for everything a step names.
//
// A platform tool NEVER writes. The authoring agent proposes a definition as
// its typed output and a human saves it; an agent that could install a
// workflow could install one that runs on a schedule, which is not a power to
// hand over as a side effect of drafting.
const (
	ToolCatalog  = "wf_catalog"
	ToolValidate = "wf_validate"
	ToolDryRun   = "wf_dryrun"
	// ToolAskHuman stops the step and asks the operator. It is named in
	// `tools:` like any other tool (ADR 0021): a capability reachable through a
	// second door is not scoped, and scoping is the security model (ADR 0004).
	// The step-level `ask_human: true` boolean still grants it, deprecated.
	ToolAskHuman = "ask_human"
)

// PlatformTools describes them for the same listings the built-ins appear in.
func PlatformTools() []BuiltinTool {
	return []BuiltinTool{
		{
			Name: ToolAskHuman,
			Description: "Ask the human operator a question and wait. The run parks in needs_input until " +
				"somebody answers, so name it only on a step that may genuinely need a person.",
		},
		{
			Name: ToolCatalog,
			Description: "List what a workflow may NAME on this machine: skills, built-in tools, providers, " +
				"classifiers and MCP servers. Call this before writing a workflow — a name that is not here " +
				"makes the whole file fail to load.",
		},
		{
			Name: ToolValidate,
			Description: "Validate a workflow definition (JSON) exactly as the loader would. Returns the " +
				"loader's own errors. A definition that does not validate cannot be saved.",
		},
		{
			Name: ToolDryRun,
			Description: "Dry run a workflow definition (JSON): resolve every name against this machine, " +
				"render every prompt, compute the execution order and the cost ceiling. Calls no model and " +
				"writes nothing. This is how you check a draft actually works before proposing it.",
		},
	}
}

// IsPlatformTool reports whether a name is one of ours rather than toolnexus's.
func IsPlatformTool(name string) bool {
	for _, t := range PlatformTools() {
		if t.Name == name {
			return true
		}
	}
	return false
}
