package workflow

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ATTACHING A FOLDER TO A RUN
//
// A run gets one directory: its checkout. That is right for a workflow that
// acts on a repository and wrong for every workflow that needs something else
// as well — a dataset, a fixtures folder, a directory to drop reports into.
//
//	mount:
//	  - /Users/me/datasets/2026:data:ro
//	  - reports:out:rw
//
// ONE LINE PER FOLDER, spelled the way docker spells it and the way anyone who
// has used docker will guess: HOST[:AT][:ro]. There is no nested object, no
// `source`/`target`/`readonly` triple and no `type`. A person can read that
// line and be right about what it does.
//
//	HOST  the folder on the machine that runs the step. ABSOLUTE, or relative —
//	      and a relative one resolves against the PLATFORM'S DATA DIR
//	      (<work dir>/mounts/<host>), never against the workspace and never
//	      against whatever directory the server process happens to be in.
//	      Against the workspace it would be a no-op (it is already there); the
//	      process's cwd is not a thing a workflow author can see or predict.
//	      The data dir gives a portable spelling that means the same thing on
//	      the server and on a worker.
//	AT    where the step sees it, relative to the workspace root. Defaults to
//	      the host folder's base name, so `- reports` is `reports` at `reports`.
//	:ro   read-only. This is the DEFAULT — a mount you did not mark is still
//	      read-only. Write `:rw` to get a writable one, which is a thing you
//	      have to type on purpose.
//
// Workflow level only. A per-step variant was deliberately not added: a folder
// the workflow needs is a property of the workflow, and nobody has yet shown a
// step that cannot work with that.
type Mount struct {
	// Host is the folder as written — absolute, or relative to the data dir.
	Host string `json:"host"`
	// At is where it appears, relative to the workspace root.
	At string `json:"at"`
	// ReadOnly is the default. See engine/mounts.go for what it means in
	// practice: the step gets a COPY, so nothing it does reaches the host.
	ReadOnly bool `json:"readOnly"`
}

// String writes the mount back as the line it was parsed from.
func (m Mount) String() string {
	s := m.Host
	if m.At != "" && m.At != defaultAt(m.Host) {
		s += ":" + m.At
	}
	if !m.ReadOnly {
		s += ":rw"
	}
	return s
}

// MarshalYAML keeps the file in the form it was written in: one string.
func (m Mount) MarshalYAML() (any, error) { return m.String(), nil }

// UnmarshalYAML accepts only the string form. A mapping is refused rather than
// quietly supported, because two spellings for one thing is how a format grows
// a manual.
func (m *Mount) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return fmt.Errorf("mount: each entry is one line, HOST[:AT][:ro] — e.g. /data/fixtures:fixtures:ro")
	}
	parsed, err := ParseMount(s)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

// MarshalJSON / UnmarshalJSON keep the API surface the same single string, so
// the Builder edits the line the file holds rather than a shape it does not.
func (m Mount) MarshalJSON() ([]byte, error) {
	return []byte(`"` + strings.ReplaceAll(m.String(), `"`, `\"`) + `"`), nil
}

func (m *Mount) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return fmt.Errorf("mount: expected the string form HOST[:AT][:ro]")
	}
	parsed, err := ParseMount(strings.ReplaceAll(s[1:len(s)-1], `\"`, `"`))
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

// ParseMount reads HOST[:AT][:ro].
//
// The fields are taken with the ACCESS MODE LAST, because the host path is the
// one that can contain a colon: `C:\data:data:ro` has to mean what the POSIX
// line means. A single-letter field followed by a slash is rejoined as a
// Windows drive.
func ParseMount(line string) (Mount, error) {
	s := strings.TrimSpace(line)
	if s == "" {
		return Mount{}, fmt.Errorf("mount: an empty entry")
	}
	m := Mount{ReadOnly: true}
	parts := splitMount(s)

	if n := len(parts); n > 1 {
		switch strings.ToLower(parts[n-1]) {
		case "ro":
			m.ReadOnly, parts = true, parts[:n-1]
		case "rw":
			m.ReadOnly, parts = false, parts[:n-1]
		}
	}
	switch len(parts) {
	case 1:
		m.Host = parts[0]
	case 2:
		m.Host, m.At = parts[0], parts[1]
	default:
		return Mount{}, fmt.Errorf("mount %q: too many fields — the shape is HOST[:AT][:ro]", line)
	}
	if m.Host == "" {
		return Mount{}, fmt.Errorf("mount %q: no folder named", line)
	}
	if m.At == "" {
		m.At = defaultAt(m.Host)
	}
	m.At = filepath.ToSlash(m.At)
	if m.At == "" {
		return Mount{}, fmt.Errorf("mount %q: nowhere to put it — say where with HOST:AT", line)
	}
	return m, nil
}

// defaultAt is the host folder's base name: `- reports` is `reports`.
func defaultAt(host string) string {
	trimmed := strings.TrimRight(filepath.ToSlash(host), "/")
	if trimmed == "" {
		return ""
	}
	base := filepath.Base(filepath.FromSlash(trimmed))
	if base == "." || base == string(filepath.Separator) || base == "/" {
		return ""
	}
	return filepath.ToSlash(base)
}

// splitMount splits on ':' and rejoins a Windows drive letter with what
// follows it, so `C:\work\data` stays one field.
func splitMount(s string) []string {
	raw := strings.Split(s, ":")
	out := make([]string, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if len(raw[i]) == 1 && isDriveLetter(raw[i][0]) && i+1 < len(raw) &&
			(strings.HasPrefix(raw[i+1], `\`) || strings.HasPrefix(raw[i+1], "/")) {
			out = append(out, raw[i]+":"+raw[i+1])
			i++
			continue
		}
		out = append(out, raw[i])
	}
	return out
}

func isDriveLetter(c byte) bool {
	c |= 0x20
	return c >= 'a' && c <= 'z'
}

// CheckMounts is the AUTHOR-TIME half of the rule. It runs at load, at save and
// in a dry run, so a workflow that could not mount safely never reaches a run.
//
// What it refuses, and why each one is a refusal rather than a warning:
//
//  1. A TEMPLATE in the host path. `{{ .Input.dir }}` as a mount is a
//     path-traversal hole with a friendly face: the run's input is chosen by
//     whoever started the run, so the folder the workflow can read would be
//     too. A mount is part of the workflow, reviewed like code, and it is
//     STATIC.
//  2. An `at` that leaves the workspace — absolute, or climbing with `..`.
//  3. Two mounts landing on the same `at`, where one would silently win.
//
// The host folder itself is checked at RUN time (engine/mounts.go), on the
// machine that will actually open it: a denylist of somebody's whole home
// directory, their keys and the system tree. That check cannot happen here,
// because `~/.ssh` on the server and `~/.ssh` on a worker are different
// folders and only one of those machines is looking.
func CheckMounts(where string, mounts []Mount) error {
	seen := map[string]string{}
	for _, m := range mounts {
		if strings.Contains(m.Host, "{{") || strings.Contains(m.At, "{{") {
			return fmt.Errorf("%s: mount %q is templated. A mount is not interpolated from run input — "+
				"whoever starts a run would be choosing which folder the workflow can read. Write the folder out",
				where, m.String())
		}
		// `~` is not expanded anywhere in this system, so `~/.ssh` would be a
		// literal folder called `~` under the data dir — an author writing it
		// means the home directory and would get something else entirely, with
		// no error. Said plainly instead.
		if strings.HasPrefix(m.Host, "~") {
			return fmt.Errorf("%s: mount %q — `~` is not expanded. Write the folder out, or use a relative name, "+
				"which resolves under the platform's data dir", where, m.String())
		}
		at := filepath.Clean(filepath.FromSlash(m.At))
		switch {
		case at == "." || at == "" || at == string(filepath.Separator):
			return fmt.Errorf("%s: mount %q would land on the workspace root itself", where, m.String())
		case filepath.IsAbs(at) || isWindowsAbsPath(at):
			return fmt.Errorf("%s: mount %q: the second field is where the STEP sees the folder, "+
				"relative to the workspace — not another absolute path", where, m.String())
		case at == ".." || strings.HasPrefix(at, ".."+string(filepath.Separator)):
			return fmt.Errorf("%s: mount %q climbs out of the workspace", where, m.String())
		}
		key := filepath.ToSlash(at)
		if prev, dup := seen[key]; dup {
			return fmt.Errorf("%s: two mounts land on %q (%s and %s); one would silently win",
				where, key, prev, m.String())
		}
		seen[key] = m.String()
	}
	return nil
}

// isWindowsAbsPath answers for a path that may have been written on another
// platform, which filepath.IsAbs cannot: it answers for the running one.
func isWindowsAbsPath(p string) bool {
	if strings.HasPrefix(p, `\\`) {
		return true
	}
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') && isDriveLetter(p[0])
}

// MountAts lists where the mounts land, slash-separated and cleaned.
func MountAts(mounts []Mount) []string {
	out := make([]string, 0, len(mounts))
	for _, m := range mounts {
		out = append(out, filepath.ToSlash(filepath.Clean(filepath.FromSlash(m.At))))
	}
	return out
}
