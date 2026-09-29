package eval

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// Result is one assertion's verdict, with the value it actually saw so a
// failing cell explains itself without anyone opening the run.
type Result struct {
	Assertion Assertion `json:"assertion"`
	Pass      bool      `json:"pass"`
	Actual    any       `json:"actual,omitempty"`
	Detail    string    `json:"detail,omitempty"`
}

// Check evaluates a against the typed outputs of a run, keyed by step id. The
// output of a step that never produced one is nil, which fails everything but
// `exists: false` — a step that did not submit cannot pass by accident.
func Check(a Assertion, outputs map[string]json.RawMessage) Result {
	r := Result{Assertion: a}
	raw, ran := outputs[a.Step]
	if !ran || len(raw) == 0 || string(raw) == "null" {
		if a.Op == "exists" && a.Value == false {
			r.Pass = true
			return r
		}
		r.Detail = fmt.Sprintf("step %s has no output", a.Step)
		return r
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		r.Detail = "output is not JSON: " + err.Error()
		return r
	}
	got, found := lookup(doc, a.Path)
	r.Actual = got
	r.Pass, r.Detail = apply(a, got, found)
	return r
}

// lookup walks a dotted path. Numeric segments index arrays; `#` is a length.
func lookup(doc any, path string) (any, bool) {
	if path == "" {
		return doc, true
	}
	cur := doc
	for _, seg := range strings.Split(path, ".") {
		if seg == "#" {
			switch t := cur.(type) {
			case []any:
				cur = float64(len(t))
			case map[string]any:
				cur = float64(len(t))
			case string:
				cur = float64(len([]rune(t)))
			default:
				return nil, false
			}
			continue
		}
		switch t := cur.(type) {
		case map[string]any:
			v, ok := t[seg]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(t) {
				return nil, false
			}
			cur = t[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

func apply(a Assertion, got any, found bool) (bool, string) {
	if a.Op == "exists" {
		want := a.Value != false
		if found == want {
			return true, ""
		}
		if want {
			return false, "path not present"
		}
		return false, "path is present"
	}
	if !found {
		return false, "path not present"
	}
	switch a.Op {
	case "eq":
		return equal(got, a.Value), ""
	case "ne":
		return !equal(got, a.Value), ""
	case "contains":
		switch t := got.(type) {
		case string:
			s, ok := a.Value.(string)
			return ok && strings.Contains(t, s), ""
		case []any:
			for _, x := range t {
				if equal(x, a.Value) {
					return true, ""
				}
			}
			return false, ""
		case map[string]any:
			// An object "contains" a key — the natural reading of `contains`
			// over a map, and the only one that needs no second value.
			s, ok := a.Value.(string)
			_, has := t[s]
			return ok && has, ""
		}
		return false, "contains needs a string, list or object"
	case "matches":
		s, ok := got.(string)
		if !ok {
			return false, "matches needs a string"
		}
		return regexp.MustCompile(a.Value.(string)).MatchString(s), ""
	case "gte", "lte":
		g, ok := number(got)
		if !ok {
			return false, a.Op + " needs a number"
		}
		w, _ := number(a.Value)
		if a.Op == "gte" {
			return g >= w, ""
		}
		return g <= w, ""
	case "in":
		for _, x := range a.Value.([]any) {
			if equal(got, x) {
				return true, ""
			}
		}
		return false, ""
	}
	return false, "unknown op " + a.Op
}

// equal compares two JSON-shaped values, treating every number as a float64 so
// that YAML's `2` equals the output's `2.0`.
func equal(a, b any) bool {
	if x, ok := number(a); ok {
		y, ok := number(b)
		return ok && x == y
	}
	return reflect.DeepEqual(normalize(a), normalize(b))
}

func number(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	}
	return 0, false
}
