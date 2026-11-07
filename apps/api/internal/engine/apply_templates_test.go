package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// INSTALLING THE SHIPPED TEMPLATES, the two ways a person does it, against the
// real templates directory: `wfx apply <t>/workflow.yaml` and
// `wfx new <t> --as <t>`.

func shippedEngine(t *testing.T) *Engine {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{
		WorkflowsDir: filepath.Join(root, "workflows"),
		TemplatesDir: repoPath(t, "templates"),
		WorkDir:      filepath.Join(root, "work"),
		SkillsDir:    filepath.Join(root, "skills"),
	}
	_ = os.MkdirAll(cfg.WorkflowsDir, 0o755)
	cat, _ := catalog.Load("", "")
	eng := New(cfg, nil, nil, map[string]*workflow.Definition{}, skills.Load(cfg.SkillsDir), cat)
	if err := eng.ReloadDefinitions(); err != nil {
		t.Fatal(err)
	}
	return eng
}

// applied decodes a template file exactly as `wfx apply` sends it: strict
// YAML → the definition → JSON on the wire → the server's decode.
func applied(t *testing.T, name string) *workflow.Definition {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoPath(t, "templates"), name, workflow.DefinitionFile))
	if err != nil {
		t.Fatal(err)
	}
	d, err := workflow.DecodeYAMLStrict(raw)
	if err != nil {
		t.Fatal(err)
	}
	j, _ := json.Marshal(d)
	back, err := workflow.DecodeDefinition(j)
	if err != nil {
		t.Fatal(err)
	}
	return back
}

// 1: `on: workflow_call` survives the save round trip. 2: a workflow that
// `uses:` another (frontdesk → load-context) saves, the callee resolved from
// what is loaded. 3: an applied template file installs as a WORKFLOW — listed,
// runnable — not as a template nobody sees.
func TestApplyingShippedTemplateFiles(t *testing.T) {
	eng := shippedEngine(t)
	for _, n := range []string{"load-context", "whatsapp-sender", "frontdesk", "retrospective"} {
		if _, err := eng.SaveWorkflow(applied(t, n)); err != nil {
			t.Fatalf("apply %s: %v", n, err)
		}
		got := eng.Definitions()[n]
		if got == nil || got.Source != "local" || got.Template.Is {
			t.Fatalf("%s after apply: %+v — want a local, runnable workflow", n, got)
		}
	}
	if _, s := eng.Definitions()["frontdesk"].Step("ctx.gather"); s == nil {
		t.Error("frontdesk lost the steps it uses from load-context")
	}
}

// 4: `wfx new <t> --as <t>` on an empty server: a template does not hold its
// name (templates name each other), the copy takes it, the template remains.
func TestCopyingATemplateUnderItsOwnName(t *testing.T) {
	eng := shippedEngine(t)
	for _, n := range []string{"load-context", "whatsapp-sender", "retrospective", "frontdesk"} {
		def, err := eng.CopyTemplate(context.Background(), n, n)
		if err != nil {
			t.Fatalf("new %s --as %s: %v", n, n, err)
		}
		if _, err := eng.SaveWorkflow(def); err != nil {
			t.Fatalf("save %s: %v", n, err)
		}
		if got := eng.Definitions()[n]; got == nil || got.Template.Is || got.Source != "local" {
			t.Fatalf("%s: the copy does not own the name: %+v", n, got)
		}
		if _, err := eng.Template(n); err != nil {
			t.Fatalf("the template %s is gone after the copy: %v", n, err)
		}
	}
	// A real workflow still holds its name.
	if _, err := eng.CopyTemplate(context.Background(), "frontdesk", "frontdesk"); err == nil {
		t.Fatal("copying onto an existing workflow must be refused")
	}
}
