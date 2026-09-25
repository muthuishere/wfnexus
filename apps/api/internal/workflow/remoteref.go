package workflow

import (
	"fmt"
	"strings"
)

// A REMOTE `use:` — a git repository and a ref, in the grammar GitHub Actions
// and Go modules already use.
//
// `use: reproduce-bug` still means "a task file next to me". `use:
// acme/bug-fix@v1.2.0` means the repository acme/bug-fix on github.com at ref
// v1.2.0. The `@` is what separates the two readings, exactly as it does in
// `uses: actions/checkout@v4` and `go install github.com/x/y@v1.2.3`.
//
// The host rules are borrowed whole, not coined:
//   - a first segment with NO dot resolves to github.com (Actions' rule);
//   - a first segment WITH a dot is the host itself (Go's module-path rule —
//     the dot is what distinguishes a host from a namespace);
//   - anything carrying a URL scheme is handed to git verbatim.
//
// There is deliberately no configurable default host, because a configurable
// default host is how a tool ends up meaning github.com.

// DefaultRemoteHost is a constant, not a setting. See above.
const DefaultRemoteHost = "github.com"

// Ref is a parsed remote reference.
type Ref struct {
	// Raw is the reference exactly as the author wrote it.
	Raw string `json:"raw"`
	// Remote is the git remote URL to hand to git.
	Remote string `json:"remote"`
	// Ref is the tag, branch or commit SHA.
	Ref string `json:"ref"`
	// Bundle selects one bundle inside a repository holding several —
	// the `#name` fragment. Empty when the repository holds exactly one.
	Bundle string `json:"bundle,omitempty"`
}

// RemotePin is what one remote reference RESOLVED to, recorded so a rerun is
// exact. The commit is the pin; the ref is only what was written, and a tag
// can move.
type RemotePin struct {
	Reference string `json:"reference"`
	Remote    string `json:"remote"`
	Commit    string `json:"commit"`
	Digest    string `json:"digest"`
}

// IsRemoteUse reports whether a `use:` value is a remote reference. A value
// with no `@` is a local task name and takes no part in any of this.
func IsRemoteUse(v string) bool { return strings.Contains(v, "@") }

// ParseRef reads a remote reference, or says why it is not one.
//
// It REFUSES anything that would name a local path: `../`, an absolute path and
// a `file://` URL. A reference is a repository somebody else can fetch; one
// that reads this machine's filesystem is a way to make a workflow mean
// something different on every host it lands on, and a way out of a checkout.
func ParseRef(v string) (Ref, error) {
	raw := v
	if !IsRemoteUse(v) {
		return Ref{}, fmt.Errorf("%q is not a remote reference: it carries no @ref", raw)
	}
	// The fragment selects a bundle inside the repository: acme/wf@v1#bug-fix.
	sel := ""
	if i := strings.Index(v, "#"); i >= 0 {
		sel, v = v[i+1:], v[:i]
		if sel == "" {
			return Ref{}, fmt.Errorf("%q: the # selects a bundle inside the repository and must name one", raw)
		}
	}
	at := strings.LastIndex(v, "@")
	if at < 0 {
		return Ref{}, fmt.Errorf("%q is not a remote reference: it carries no @ref", raw)
	}
	path, ref := v[:at], v[at+1:]
	if ref == "" {
		return Ref{}, fmt.Errorf("%q: the text after @ is the ref — a tag, a branch or a commit — and it is empty", raw)
	}
	if path == "" {
		return Ref{}, fmt.Errorf("%q: the text before @ is the repository and it is empty", raw)
	}

	// A reference must not name a local path or climb out of a checkout.
	if err := refuseLocalPath(raw, path); err != nil {
		return Ref{}, err
	}

	if strings.Contains(path, "://") || isSCPLike(path) {
		// An explicit URL is the escape hatch: a bare repo on a NAS, a gitolite
		// host, a git daemon. Handed to git verbatim.
		return Ref{Raw: raw, Remote: path, Ref: ref, Bundle: sel}, nil
	}

	segs := strings.Split(path, "/")
	for _, s := range segs {
		if s == "" {
			return Ref{}, fmt.Errorf("%q: the repository path has an empty segment", raw)
		}
	}
	var host string
	if strings.Contains(segs[0], ".") {
		host, segs = segs[0], segs[1:]
	} else {
		host = DefaultRemoteHost
	}
	if len(segs) < 2 {
		return Ref{}, fmt.Errorf("%q: %s needs an owner and a repository, as in acme/bug-fix@v1.2.0", raw, host)
	}
	return Ref{
		Raw:    raw,
		Remote: "https://" + host + "/" + strings.Join(segs, "/") + ".git",
		Ref:    ref,
		Bundle: sel,
	}, nil
}

// isSCPLike spots git's other remote spelling, git@host:org/repo — a colon
// before any slash, which is what separates it from a Windows drive letter
// (engine/sources.go draws the same line, for the same reason).
func isSCPLike(path string) bool {
	i := strings.Index(path, ":")
	if i <= 1 {
		return false
	}
	slash := strings.Index(path, "/")
	return (slash < 0 || i < slash) && strings.Contains(path[:i], "@")
}

// refuseLocalPath is the security rule, stated once — and it ENUMERATES WHAT
// IS ALLOWED, not what is refused.
//
// That is this repo's own rule (docs/not-now.md): every mechanism that fails
// enumerates escapes, every mechanism that works enumerates inclusions. The
// first version of this function listed bad things — file://, absolute paths,
// "..", a drive letter — and missed `ext::`, which is not a path at all:
//
//	use: ext::sh -c <command> @v1
//
// git's ext transport RUNS THAT COMMAND. A `use:` comes out of a workflow file
// that, under this change, somebody else published, so that is the untrusted
// input path and an escape list was never going to hold it. An allowlist
// refuses ext:: and every transport helper git gains later, without us
// learning their names.
//
// file:// IS allowed, deliberately: a bare repo on a mounted path is how an
// air-gapped site actually works, and task 9.3 requires a non-GitHub remote.
// The danger the escape list was reaching for is a BARE path or a traversal
// being read as a remote; an explicit file:// URL is an operator naming a
// remote on purpose, exactly like ssh://.
func refuseLocalPath(raw, path string) error {
	bad := func(what string) error {
		return fmt.Errorf("%q: a remote reference names a repository, not %s — "+
			"write it as owner/repo@ref, host/owner/repo@ref, or an explicit "+
			"https://, ssh://, git:// or file:// URL", raw, what)
	}

	// A scheme is anything before "://" or before the first ":" — which is how
	// `ext::` and `transport::` present themselves.
	if i := strings.Index(path, ":"); i > 0 {
		scheme := strings.ToLower(path[:i])
		switch scheme {
		case "https", "http", "ssh", "git", "file":
			return nil // an explicit remote URL, handed to git as written
		default:
			// scp-style is `user@host:org/repo` and has no scheme: the colon
			// follows a host, and a host cannot contain a "/" before it.
			if !strings.Contains(path[:i], "/") && strings.Contains(path[:i], "@") {
				return nil
			}
			// A windows drive letter is a single character before the colon.
			if len(scheme) == 1 {
				return bad("an absolute path")
			}
			return bad("a " + scheme + ":: transport, which can run a command")
		}
	}

	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\") {
		return bad("an absolute path")
	}
	if strings.HasPrefix(path, "~") {
		return bad("a home-relative path")
	}
	for _, seg := range strings.Split(strings.ReplaceAll(path, "\\", "/"), "/") {
		if seg == ".." || seg == "." {
			return bad("a path relative to this machine")
		}
	}
	return nil
}
