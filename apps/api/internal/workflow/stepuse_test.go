package workflow

import (
	"reflect"
	"strings"
	"testing"
)

func TestStepUsesExpandsInPlaceWithTypedWith(t *testing.T) {
	defs, err := loadWithTask(t, `
name: wf
steps:
  - id: first
    run: echo start
  - id: hello
    uses: greet@2
    needs: [first]
    with: { who: ops, times: 2 }
  - id: after
    needs: [hello]
    run: echo done
`)
	if err != nil {
		t.Fatal(err)
	}
	st := defs["wf"].Steps
	var ids []string
	for _, s := range st {
		ids = append(ids, s.ID)
	}
	if !reflect.DeepEqual(ids, []string{"first", "hello.say", "after"}) {
		t.Fatalf("ids = %v", ids)
	}
	if !strings.Contains(filledStep(t, st[1], nil), "ops x2 plain") {
		t.Fatalf("run = %q", st[1].Run)
	}
	if !reflect.DeepEqual(st[1].Needs, []string{"first"}) || !reflect.DeepEqual(st[2].Needs, []string{"hello.say"}) {
		t.Fatalf("needs: call=%v after=%v", st[1].Needs, st[2].Needs)
	}
}

func TestStepUsesInsideAJob(t *testing.T) {
	defs, err := loadWithTask(t, `
name: wf
jobs:
  j:
    steps:
      - id: a
        run: echo a
      - id: g
        uses: greet
        with: { who: dev }
`)
	if err != nil {
		t.Fatal(err)
	}
	st := defs["wf"].Steps
	if len(st) != 2 || st[1].ID != "j.g.say" || !reflect.DeepEqual(st[1].Needs, []string{"j.a"}) || !strings.Contains(filledStep(t, st[1], nil), "dev x1") {
		t.Fatalf("steps = %+v", st)
	}
}

func TestStepUsesValidatedAtLoad(t *testing.T) {
	cases := map[string]string{
		"uses: greet\n    with: { times: 2 }":            `wf step g: use "greet" (as g): input "who" is required`,
		"uses: greet\n    with: { who: 1 }":              `input "who": want string`,
		"uses: greet@3\n    with: { who: x }":            `pins major 3`,
		"uses: nope\n    with: { who: x }":               `unknown task "nope"`,
		"uses: greet\n    run: echo\n    with: {who: x}": `may not also carry`,
		"run: echo\n    with: { who: x }":                "`with:` on a step needs `uses:`",
	}
	for in, want := range cases {
		_, err := loadWithTask(t, "name: wf\nsteps:\n  - id: g\n    "+in+"\n")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", in, err, want)
		}
	}
}
