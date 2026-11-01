package workflow

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMemoryYAML(t *testing.T) {
	var off struct {
		Memory *Memory `yaml:"memory"`
	}
	if err := yaml.Unmarshal([]byte("memory: false\n"), &off); err != nil || off.Memory == nil || !off.Memory.Off {
		t.Fatalf("memory: false → %+v (%v)", off.Memory, err)
	}
	var obj struct {
		Memory *Memory `yaml:"memory"`
	}
	src := "memory:\n  include: [workflow:triage, project]\n  budget: 800\n  retain: 5\n"
	if err := yaml.Unmarshal([]byte(src), &obj); err != nil {
		t.Fatal(err)
	}
	if obj.Memory.Off || obj.Memory.Budget != 800 || obj.Memory.Retain != 5 || len(obj.Memory.Include) != 2 {
		t.Fatalf("object form: %+v", obj.Memory)
	}
	out, _ := yaml.Marshal(off)
	if !strings.Contains(string(out), "memory: false") {
		t.Fatalf("round trip lost the opt-out: %s", out)
	}
	// The API dialect too.
	var j struct {
		Memory *Memory `json:"memory"`
	}
	if err := json.Unmarshal([]byte(`{"memory":false}`), &j); err != nil || !j.Memory.Off {
		t.Fatalf("json false: %+v %v", j.Memory, err)
	}
	b, _ := json.Marshal(j)
	if string(b) != `{"memory":false}` {
		t.Fatalf("json round trip: %s", b)
	}
}

func TestMemorySources(t *testing.T) {
	for in, want := range map[string]MemorySource{
		"workflow:triage":       {Kind: "workflow", Workflow: "triage"},
		"workflow:other/deploy": {Kind: "workflow", Project: "other", Workflow: "deploy"},
		"project":               {Kind: "project"},
		"global":                {Kind: "global"},
	} {
		got, err := ParseMemorySource(in)
		if err != nil || got != want {
			t.Fatalf("%s → %+v (%v)", in, got, err)
		}
	}
	for _, bad := range []string{"", "workflow:", "everything", "workflow:/x", "workflow:a/b/c"} {
		if _, err := ParseMemorySource(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestEffectiveMemory(t *testing.T) {
	if p := EffectiveMemory(&Definition{}, nil); !p.On || p.Budget != DefaultMemoryBudget || p.Retain != DefaultMemoryRetain {
		t.Fatalf("default: %+v", p)
	}
	off := &Definition{Memory: &Memory{Off: true}}
	// A step cannot switch on what its workflow switched off.
	if p := EffectiveMemory(off, &Step{Memory: &Memory{Budget: 10}}); p.On {
		t.Fatal("a step re-enabled memory the workflow disabled")
	}
	wide := &Definition{Memory: &Memory{Include: []string{"workflow:a", "project"}, Budget: 2000}}
	p := EffectiveMemory(wide, &Step{Memory: &Memory{Include: []string{"workflow:a", "global"}, Budget: 5000}})
	if len(p.Include) != 1 || p.Include[0] != "workflow:a" {
		t.Fatalf("a step widened the include list: %v", p.Include)
	}
	if p.Budget != 2000 {
		t.Fatalf("a step raised the budget: %d", p.Budget)
	}
	if p := EffectiveMemory(wide, &Step{Memory: &Memory{Off: true}}); p.On {
		t.Fatal("step opt-out ignored")
	}
}

func TestMemoryValidation(t *testing.T) {
	def := &Definition{Name: "m", Memory: &Memory{Include: []string{"nonsense"}},
		Steps: []Step{{ID: "a", Run: "true"}}}
	if err := def.validate(nil); err == nil || !strings.Contains(err.Error(), "memory include") {
		t.Fatalf("bad include accepted: %v", err)
	}
}
