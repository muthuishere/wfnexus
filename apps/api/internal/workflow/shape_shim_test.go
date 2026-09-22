package workflow

import (
	"strings"
	"testing"
)

// The shape is reflected, not written down, so a new field appears in it
// automatically — a hand-maintained copy would go stale and teach an agent a
// field that does not exist.
func TestDescribeShapeNamesTheRealFields(t *testing.T) {
	s := DescribeShape()
	for _, want := range []string{"steps", "output_schema", "prompt", "run", "judge", "soul", "provider", "needs", "on"} {
		if !strings.Contains(s, want) {
			t.Errorf("the shape does not mention %q", want)
		}
	}
	// And it must NOT invite the names an agent guessed from other formats.
	for _, no := range []string{"\ttitle ", " type ", "\tinput "} {
		if strings.Contains(s, no) {
			t.Errorf("the shape appears to offer %q, which is not a field", strings.TrimSpace(no))
		}
	}
}

// yaml and json silently drop an unknown key, so `title:` instead of `name:`
// surfaces later as something unrelated. This is what turns that into an
// answerable error.
func TestUnknownStepFieldsCatchesGuessedNames(t *testing.T) {
	got := UnknownStepFields(map[string]any{
		"id": "a", "name": "A", "prompt": "hi", "output_schema": map[string]any{},
		"title": "A", "type": "agent", "input": map[string]any{}, "question": "x",
	})
	want := []string{"input", "question", "title", "type"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("UnknownStepFields = %v, want %v", got, want)
	}
	if n := UnknownStepFields(map[string]any{"id": "a", "prompt": "x", "needs": []any{}}); len(n) != 0 {
		t.Fatalf("real fields reported as unknown: %v", n)
	}
}

// A definition arrives in two dialects — a FILE says `output_schema`, the API
// says `outputSchema` — and dropping the one we did not expect reported a step
// as missing a field it had written correctly. It survived three live authoring
// runs before being understood.
func TestDecodeDefinitionAcceptsBothDialects(t *testing.T) {
	for _, src := range []string{
		`{"name":"wf","steps":[{"id":"a","prompt":"go","max_turns":7,"output_schema":{"type":"object"},"requires_approval":true}]}`,
		`{"name":"wf","steps":[{"id":"a","prompt":"go","maxTurns":7,"outputSchema":{"type":"object"},"requiresApproval":true}]}`,
	} {
		def, err := DecodeDefinition([]byte(src))
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		s := def.Steps[0]
		if s.OutputSchema == nil {
			t.Errorf("output schema dropped from %s", src)
		}
		if s.MaxTurns != 7 || !s.RequiresApproval {
			t.Errorf("fields lost from %s: turns=%d approval=%v", src, s.MaxTurns, s.RequiresApproval)
		}
	}
}

// Normalising field names must never touch a key the AUTHOR chose. The first
// version renamed a judge question called `is_security` to `isSecurity`, so the
// gate referencing it by its real name stopped resolving and a working
// workflow no longer loaded.
func TestDecodeDefinitionLeavesAuthorVocabularyAlone(t *testing.T) {
	def, err := DecodeDefinition([]byte(`{
	  "name":"wf",
	  "input_schema":{"type":"object","properties":{"repo_path":{"type":"string"}}},
	  "steps":[{
	    "id":"a","prompt":"go",
	    "output_schema":{"type":"object","properties":{"is_security":{"type":"boolean"},"file_path":{"type":"string"}}},
	    "judge":{"state":"s","questions":{"is_security":{"type":"noul","instructions":"i"}}}
	  }]}`))
	if err != nil {
		t.Fatal(err)
	}
	props, _ := def.InputSchema["properties"].(map[string]any)
	if _, ok := props["repo_path"]; !ok {
		t.Errorf("an input property was renamed: %v", props)
	}
	out, _ := def.Steps[0].OutputSchema["properties"].(map[string]any)
	for _, want := range []string{"is_security", "file_path"} {
		if _, ok := out[want]; !ok {
			t.Errorf("output property %q was renamed: %v", want, out)
		}
	}
	if _, ok := def.Steps[0].Judge.Questions["is_security"]; !ok {
		t.Errorf("a judge question was renamed: %v", def.Steps[0].Judge.Questions)
	}
}
