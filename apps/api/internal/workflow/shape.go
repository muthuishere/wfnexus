package workflow

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Shape describes the workflow file's own structure, derived from the Go types
// by reflection.
//
// It is reflected rather than written down because a hand-maintained copy of a
// struct goes stale the first time somebody adds a field, and the reader here
// is an agent authoring a workflow — it will believe whatever it is told. A
// stale list is worse than none: it teaches a field that does not exist.
//
// This is the answer to a real failure. An authoring agent given the
// catalogues but not the shape produced steps with `title`, `type`, `input` and
// `question` — plausible names from other workflow formats, none of them ours —
// and spent twelve dry runs failing to work out why.

// Field is one key in the file.
type Field struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Doc      string  `json:"doc,omitempty"`
	Required bool    `json:"required,omitempty"`
	Fields   []Field `json:"fields,omitempty"`
}

// DescribeShape renders the workflow file's structure as text an agent can
// follow: the names it may use, their types, and what each one is for.
func DescribeShape() string {
	var b strings.Builder
	b.WriteString("A WORKFLOW FILE\n\n")
	b.WriteString(describe(reflect.TypeOf(Definition{}), 0, map[reflect.Type]bool{}))
	b.WriteString("\nNOTES\n")
	b.WriteString("  - A step is an agent (`prompt`), a command (`run`) or a decision (`judge`). Exactly one.\n")
	b.WriteString("  - `name` is the human name of a step; there is no `title`, no `type` and no `input` field.\n")
	b.WriteString("  - A step reads an earlier step with {{ .Steps.<step-id>.<field> }}, and that field must\n")
	b.WriteString("    appear in that step's output_schema. Reading a later step renders empty.\n")
	b.WriteString("  - `output_schema` is a JSON Schema object and is what the step is forced to return.\n")
	b.WriteString("  - Every question under `judge.questions` NEEDS `instructions` — what the judge is\n")
	b.WriteString("    being asked, in words. A question without them is refused at load.\n")
	b.WriteString("  - A `run` step needs an `output_schema` too; it gets ok/exitCode/stdout/stderr.\n")
	return b.String()
}

// describe walks a struct, skipping anything not serialised.
func describe(t reflect.Type, depth int, seen map[reflect.Type]bool) string {
	if depth > 3 || seen[t] {
		return ""
	}
	seen[t] = true
	defer delete(seen, t)

	pad := strings.Repeat("  ", depth+1)
	var b strings.Builder
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("yaml")
		if tag == "" {
			tag = f.Tag.Get("json")
		}
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" || !f.IsExported() {
			continue
		}
		fmt.Fprintf(&b, "%s%-18s %s\n", pad, name, typeName(f.Type))

		inner := f.Type
		for inner.Kind() == reflect.Ptr || inner.Kind() == reflect.Slice {
			inner = inner.Elem()
		}
		if inner.Kind() == reflect.Struct && inner.PkgPath() != "" {
			b.WriteString(describe(inner, depth+1, seen))
		}
	}
	return b.String()
}

func typeName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Ptr:
		return typeName(t.Elem()) + " (optional)"
	case reflect.Slice:
		return "list of " + typeName(t.Elem())
	case reflect.Map:
		return "map"
	case reflect.Struct:
		return "object"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int64:
		return "number"
	case reflect.Interface:
		return "any"
	}
	return t.Kind().String()
}

// KnownFields is every key a Step may carry, for rejecting a typo.
func KnownFields(v any) map[string]bool {
	out := map[string]bool{}
	t := reflect.TypeOf(v)
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		for _, key := range []string{"yaml", "json"} {
			if name := strings.Split(f.Tag.Get(key), ",")[0]; name != "" && name != "-" {
				out[name] = true
			}
		}
	}
	return out
}

// UnknownStepFields reports keys that are not fields of Step.
//
// yaml and json both IGNORE an unknown key, so `title:` instead of `name:` is
// silently dropped and the failure surfaces later as something unrelated —
// which is exactly what happened: an agent wrote `title` and `type` and was
// told its step needed an output_schema it had in fact provided under a name
// nothing reads.
func UnknownStepFields(raw map[string]any) []string {
	known := KnownFields(Step{})
	var out []string
	for k := range raw {
		if !known[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
