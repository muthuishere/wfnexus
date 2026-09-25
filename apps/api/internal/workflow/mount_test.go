package workflow

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseMountShapes(t *testing.T) {
	cases := []struct {
		line string
		want Mount
	}{
		// A bare folder: it lands under its own name, read-only.
		{"/data/fixtures", Mount{Host: "/data/fixtures", At: "fixtures", ReadOnly: true}},
		{"/data/fixtures:in", Mount{Host: "/data/fixtures", At: "in", ReadOnly: true}},
		{"/data/fixtures:in:ro", Mount{Host: "/data/fixtures", At: "in", ReadOnly: true}},
		{"/data/out:out:rw", Mount{Host: "/data/out", At: "out", ReadOnly: false}},
		// Relative: the portable spelling, resolved under the data dir.
		{"reports:out", Mount{Host: "reports", At: "out", ReadOnly: true}},
		// A Windows drive letter is not a field separator.
		{`C:\work\data:data:ro`, Mount{Host: `C:\work\data`, At: "data", ReadOnly: true}},
	}
	for _, c := range cases {
		got, err := ParseMount(c.line)
		if err != nil {
			t.Fatalf("%q: %v", c.line, err)
		}
		if got != c.want {
			t.Errorf("%q → %+v, want %+v", c.line, got, c.want)
		}
	}
}

// Read-only is the DEFAULT, not a thing you opt into. A mount nobody thought
// about must be the safe one.
func TestAMountIsReadOnlyUnlessItSaysOtherwise(t *testing.T) {
	m, err := ParseMount("/data/x")
	if err != nil {
		t.Fatal(err)
	}
	if !m.ReadOnly {
		t.Fatal("a mount with no mode must be read-only")
	}
}

func TestMountRoundTripsThroughYAML(t *testing.T) {
	var got struct {
		Mount []Mount `yaml:"mount"`
	}
	src := "mount:\n  - /data/fixtures:in:ro\n  - reports:out:rw\n"
	if err := yaml.Unmarshal([]byte(src), &got); err != nil {
		t.Fatal(err)
	}
	back, err := yaml.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	// `:ro` is written back as nothing, because nothing MEANS read-only. The
	// shortest form that carries the same meaning is the one the file keeps.
	for _, want := range []string{"/data/fixtures:in", "reports:out:rw"} {
		if !strings.Contains(string(back), want) {
			t.Fatalf("%q did not survive the round trip:\n%s", want, back)
		}
	}
}

// A mapping is refused outright: one spelling per thing.
func TestMountRefusesTheObjectForm(t *testing.T) {
	var got struct {
		Mount []Mount `yaml:"mount"`
	}
	err := yaml.Unmarshal([]byte("mount:\n  - host: /data\n    at: in\n"), &got)
	if err == nil {
		t.Fatal("the nested object form must be refused")
	}
}

// THE RULE, at author time. Each of these is a hole if it is allowed.
func TestCheckMountsRefusals(t *testing.T) {
	cases := []struct {
		name, line, want string
	}{
		{"templated from run input", "{{ .Input.dir }}:data", "templated"},
		{"templated destination", "/data:{{ .Input.at }}", "templated"},
		{"absolute destination", "/data:/etc", "relative to the workspace"},
		{"climbing out", `/data:../../elsewhere`, "climbs out"},
		{"the workspace root itself", "/data:.", "workspace root"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := ParseMount(c.line)
			if err != nil {
				// Some of these are refused at parse time, which is also fine.
				return
			}
			err = CheckMounts("wf", []Mount{m})
			if err == nil {
				t.Fatalf("%q must be refused", c.line)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%q: error %q does not say %q", c.line, err, c.want)
			}
		})
	}
}

func TestCheckMountsRefusesTwoFoldersOnOneSpot(t *testing.T) {
	a, _ := ParseMount("/data/one:shared")
	b, _ := ParseMount("/data/two:shared")
	if err := CheckMounts("wf", []Mount{a, b}); err == nil {
		t.Fatal("two mounts on one destination must be refused; one would silently win")
	}
}

func TestCheckMountsAcceptsTheOrdinaryCase(t *testing.T) {
	a, _ := ParseMount("/data/fixtures:in:ro")
	b, _ := ParseMount("reports:out:rw")
	if err := CheckMounts("wf", []Mount{a, b}); err != nil {
		t.Fatal(err)
	}
}

// `./x` is beside the WORKFLOW FILE, which is the one place a workflow's own
// fixtures can live and still travel with it. The source is either a local
// directory or a git working copy, and one spelling covers both — a source is a
// source. It is spelled the way docker-compose spells file-relative, because
// that is what anyone will guess it means.
func TestASourceRelativeMountIsParsedAsSuch(t *testing.T) {
	for _, line := range []string{"./fixtures", ".\\fixtures"} {
		m, err := ParseMount(line)
		if err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		if !m.FromSource {
			t.Errorf("%q was not recorded as source-relative", line)
		}
		if m.Host != "fixtures" {
			t.Errorf("%q → host %q, want the prefix stripped", line, m.Host)
		}
		if m.At != "fixtures" {
			t.Errorf("%q → at %q, want the base name", line, m.At)
		}
		if !m.ReadOnly {
			t.Errorf("%q is writable; read-only is the default and must stay it", line)
		}
	}

	// A bare relative host is UNCHANGED: still the data dir, as before. Redefining
	// it would change what every existing workflow means.
	m, err := ParseMount("reports:out:rw")
	if err != nil {
		t.Fatal(err)
	}
	if m.FromSource {
		t.Error("a bare relative host was treated as source-relative; that would silently move every existing mount")
	}
	if m.Host != "reports" || m.At != "out" || m.ReadOnly {
		t.Errorf("the existing spelling changed meaning: %+v", m)
	}
}

// [SEC-TEST] A source-relative mount may not climb out of the source. The whole
// value of the spelling is meaning the same thing wherever the workflow travels;
// `./../../etc` would mean something different on every machine and something
// dangerous on some.
func TestASourceRelativeMountCannotEscapeTheSource(t *testing.T) {
	for _, bad := range []string{
		"./../secrets",
		"./a/../../etc",
		"./..",
		"./.",
		`.\..\secrets`,
		"./",
	} {
		if m, err := ParseMount(bad); err == nil {
			t.Errorf("%q was accepted as %+v — it reaches outside the workflow's own directory", bad, m)
		}
	}
	// A folder honestly named with leading dots is not an escape: the check is on
	// segments, not on whether the string contains "..".
	for _, ok := range []string{"./..data", "./data..", "./a/..b/c"} {
		if _, err := ParseMount(ok); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
}

// A source-relative mount ROUND-TRIPS. String() is what a save writes, so losing
// the `./` would rewrite a folder beside the workflow as a folder under the data
// dir — the same line quietly meaning a different directory after one save.
func TestASourceRelativeMountRoundTrips(t *testing.T) {
	for _, line := range []string{"./fixtures", "./fixtures:data", "./fixtures:data:rw", "./a/b:at"} {
		m, err := ParseMount(line)
		if err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		again, err := ParseMount(m.String())
		if err != nil {
			t.Fatalf("%q → %q did not parse back: %v", line, m.String(), err)
		}
		if again != m {
			t.Errorf("%q → %q → %+v, want %+v", line, m.String(), again, m)
		}
		if !strings.HasPrefix(m.String(), "./") {
			t.Errorf("%q printed as %q — the ./ was lost, so a save would change which folder it means", line, m.String())
		}
	}
}
