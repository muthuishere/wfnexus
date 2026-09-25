package engine

import "github.com/muthuishere/wfnexus/apps/api/internal/catalog"

// installers is the allowlist of commands setup may run to put a binary on
// PATH. It is data, and it is closed: a preset that is not a key here is not
// installed, it is reported with an instruction. Nothing in a workflow, a
// bundle or any other published content is consulted — an install command
// that arrived that way would be remote code execution by publication, the
// ext:: refusal pointed at a different door.
//
// A nil Command means we know the vendor and will not install it. The
// instruction is what the operator runs. A wrong command is worse than none,
// so an entry exists only for the vendor's own documented installer.
var installers = map[string]Installer{
	"claude": {
		Command:     []string{"sh", "-c", "curl -fsSL https://claude.ai/install.sh | bash"},
		Instruction: "curl -fsSL https://claude.ai/install.sh | bash",
	},
	"codex": {
		Command:     []string{"npm", "install", "-g", "@openai/codex"},
		Instruction: "npm install -g @openai/codex",
	},
	"opencode": {
		Command:     []string{"sh", "-c", "curl -fsSL https://opencode.ai/install | bash"},
		Instruction: "curl -fsSL https://opencode.ai/install | bash",
	},
	"devin": {
		Command:     []string{"sh", "-c", "curl -fsSL https://cli.devin.ai/install.sh | bash"},
		Instruction: "curl -fsSL https://cli.devin.ai/install.sh | bash",
	},
	"copilot": {
		Command:     []string{"npm", "install", "-g", "@github/copilot"},
		Instruction: "npm install -g @github/copilot",
	},
	// gh has no single cross-platform installer. Naming one would be a guess,
	// and a guess that runs is the thing this table exists to prevent.
	"gh": {
		Instruction: "install the GitHub CLI from https://cli.github.com, then run `gh auth login`",
	},
}

// Installer is one enumerated entry. Command is argv, never a string from
// outside this file. Instruction is what we print; it is not executed unless
// Command is set, and Command is set only here.
type Installer struct {
	Command     []string
	Instruction string
}

// LookupInstaller returns the entry for a preset or binary name. A miss is
// not an invitation to improvise: the caller reports the absence and runs
// nothing.
func LookupInstaller(name string) (Installer, bool) {
	ins, ok := installers[name]
	return ins, ok
}

// SetupNeed is what the platform tells a machine to prepare for: a name, a
// kind, and where a binary or an env var name applies. It has no field a
// command, a hook or a credential could travel in. The runner's installer
// table is keyed by Preset or Binary, both of which are names, and a name
// that is not in that table installs nothing.
type SetupNeed struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Preset    string `json:"preset,omitempty"`
	Binary    string `json:"binary,omitempty"`
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`
}

// SetupNeeds is the platform's provider list, reduced to what a machine can
// prepare for. The catalog's command argv is not copied: that array is how a
// published registry entry would otherwise become a command this binary runs.
func (e *Engine) SetupNeeds() []SetupNeed {
	if e.catalog == nil {
		return nil
	}
	out := make([]SetupNeed, 0, len(e.catalog.Providers.List()))
	for _, p := range e.catalog.Providers.List() {
		need := SetupNeed{
			Name: p.Name, Kind: string(p.Kind), Preset: p.Preset, APIKeyEnv: p.APIKeyEnv,
		}
		if p.Kind == catalog.KindCLI || p.Kind == catalog.KindACP {
			need.Binary = providerBinary(p)
		}
		out = append(out, need)
	}
	return out
}
