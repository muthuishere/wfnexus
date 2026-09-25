package engine

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// MAKING A MOUNT REAL, HONESTLY
//
// There is no container here. A `run:` step is a process on this machine and an
// agent step is a loop in this process, so "mount" cannot mean what it means to
// docker, and pretending otherwise would be the lie that matters: an author
// would write `:ro` and believe the folder was protected by the kernel.
//
// What it actually is, and the reason for each:
//
//	:ro (the default)   the folder is COPIED into the workspace.
//	                    Unprivileged read-only bind mounts do not exist portably
//	                    — `mount --bind -o ro` needs root on Linux and has no
//	                    macOS equivalent — and a symlink marked read-only is
//	                    read-only in the documentation only. A copy is not a
//	                    figure of speech: the step can write to it all it likes
//	                    and the host folder is untouched, which is the whole of
//	                    what read-only is supposed to buy. The cost is the copy,
//	                    so it is capped and the cap says what to do instead.
//
//	:rw                 the folder is SYMLINKED into the workspace, and its real
//	                    path is added to the run's containment roots so the
//	                    agent may follow the link. This is genuine two-way
//	                    access to the user's folder, which is why it is the
//	                    thing you have to type.
//
// ON A WORKER, the mount SPEC travels and is resolved against THAT machine's
// disk — exactly as `${GITHUB_PAT}` already resolves against the machine that
// holds it. Nothing is copied over the wire. A worker on another host cannot
// see the server's disk, so an absolute mount that does not exist there fails
// loudly and names the host, rather than running against a folder that is not
// the one the author meant. A RELATIVE mount is the portable spelling: it
// resolves under each machine's own data dir and therefore means the same
// thing everywhere.

const (
	// maxMountCopyBytes / maxMountCopyFiles cap a read-only mount's copy. Past
	// this, copying is the wrong answer and the error says so.
	maxMountCopyBytes = 512 << 20
	maxMountCopyFiles = 20000
)

// MountsDirName is where a relative mount host resolves, under the data dir.
const MountsDirName = "mounts"

// attached is one mount, resolved and in place.
type attached struct {
	Spec workflow.Mount
	// Host is the resolved absolute folder on this machine.
	Host string
	// Dest is where it now appears inside the workspace.
	Dest string
}

// attachMounts resolves and attaches every mount, returning the extra
// containment roots the agent is allowed to reach and the env the step gets.
//
// It is the SAME function on the server and on a worker, which is the only way
// a mount can be guaranteed to mean the same thing in both places.
func attachMounts(workspace, dataDir, sourceDir string, mounts []workflow.Mount) ([]attached, []string, map[string]string, error) {
	if len(mounts) == 0 {
		return nil, nil, nil, nil
	}
	if workspace == "" {
		return nil, nil, nil, fmt.Errorf("mount: this run has no workspace to attach a folder to")
	}
	var out []attached
	var roots []string
	env := map[string]string{}
	for _, m := range mounts {
		host, err := resolveMountHostFrom(dataDir, sourceDir, m)
		if err != nil {
			return nil, nil, nil, err
		}
		dest := filepath.Join(workspace, filepath.FromSlash(m.At))
		// The destination is re-checked against the RESOLVED workspace, not
		// only against the spelling CheckMounts saw. A workspace that is itself
		// a symlink, or an `at` whose parent is one, would otherwise land the
		// folder somewhere else entirely.
		if outside(resolveRoot(workspace), dest) {
			return nil, nil, nil, fmt.Errorf("mount %q would land outside the workspace", m.String())
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return nil, nil, nil, err
		}
		if err := placeMount(host, dest, m); err != nil {
			return nil, nil, nil, fmt.Errorf("mount %q: %w", m.String(), err)
		}
		if !m.ReadOnly {
			// Only a writable mount widens what the agent may touch. A
			// read-only one is a copy that is already inside the workspace, so
			// it needs no extra root — which is the quiet second benefit of
			// making read-only a copy.
			roots = append(roots, resolveRoot(host))
		}
		env[mountEnvName(m.At)] = dest
		out = append(out, attached{Spec: m, Host: host, Dest: dest})
	}
	return out, roots, env, nil
}

// placeMount puts the folder where the step will see it.
func placeMount(host, dest string, m workflow.Mount) error {
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	if !m.ReadOnly {
		return os.Symlink(host, dest)
	}
	return copyTree(host, dest)
}

// mountEnvName is the variable a step can read instead of hardcoding the
// destination: `- /data/fixtures:fixtures:ro` gives WFX_MOUNT_FIXTURES.
func mountEnvName(at string) string {
	var b strings.Builder
	b.WriteString("WFX_MOUNT_")
	for _, r := range strings.ToUpper(at) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// resolveMountHost turns the host field into an absolute folder on THIS
// machine, and applies the rule.
//
// THE RULE, in one sentence: a mount is an absolute folder that exists, or a
// name under this machine's own mounts dir, it is never anybody's home
// directory, their keys or the system tree, and if the operator has set
// WFX_MOUNT_ROOTS then it is under one of those roots or it is refused.
func resolveMountHost(dataDir string, m workflow.Mount) (string, error) {
	return resolveMountHostFrom(dataDir, "", m)
}

// resolveMountHostFrom resolves a mount, with sourceDir being the directory the
// workflow was loaded from — needed only by a `./` mount.
//
// A `./` mount is refused when sourceDir is empty rather than falling back to
// the data dir. The fallback would be the dangerous kind of convenient: on a
// WORKER there is no source directory, and silently reading `<data>/mounts/x`
// instead of the folder beside the workflow would attach the wrong data, or
// empty data, and the run would carry on as if it had the right thing.
func resolveMountHostFrom(dataDir, sourceDir string, m workflow.Mount) (string, error) {
	if m.FromSource {
		if sourceDir == "" {
			return "", fmt.Errorf("mount %q: a `./` folder is beside the workflow file, and this process does not have the workflow's own directory — "+
				"a step with `runs-on:` runs on a machine that has no copy of it, so mount an absolute path or the relative form instead", m.String())
		}
		host := filepath.Join(sourceDir, filepath.FromSlash(m.Host))
		root := resolveRoot(sourceDir)
		// The escape rule again, on the RESOLVED path. ParseMount refused the
		// spelling; this refuses a symlink inside the source that points out of
		// it, which the spelling cannot see.
		resolved := resolveRoot(host)
		if outside(root, resolved) {
			return "", fmt.Errorf("mount %q: it resolves to %s, outside the workflow's own directory", m.String(), resolved)
		}
		st, err := os.Stat(host)
		if err != nil {
			return "", fmt.Errorf("mount %q: %s is not beside the workflow file", m.String(), host)
		}
		if !st.IsDir() {
			return "", fmt.Errorf("mount %q: %s is a file, not a folder", m.String(), host)
		}
		if err := forbiddenMount(resolved); err != nil {
			return "", fmt.Errorf("mount %q: %w", m.String(), err)
		}
		return resolved, nil
	}
	host := filepath.FromSlash(m.Host)
	if !filepath.IsAbs(host) {
		if dataDir == "" {
			return "", fmt.Errorf("mount %q: a relative folder resolves under the platform's data dir, and this process has none", m.String())
		}
		base := filepath.Join(dataDir, MountsDirName)
		// The base is created BEFORE the comparison. Comparing a resolved root
		// against an unresolved one is the macOS /var-is-/private/var bug that
		// containment.go has already been bitten by twice; a directory that
		// exists resolves, and then both sides are speaking about the same path.
		if err := os.MkdirAll(base, 0o755); err != nil {
			return "", err
		}
		root := resolveRoot(base)
		host = filepath.Join(root, host)
		// Join cleans `..` away, but only after it has already moved: the
		// result is compared back against the base, so `../../etc` cannot use
		// the relative form as a way around the absolute checks.
		if outside(root, host) {
			return "", fmt.Errorf("mount %q: a relative folder stays under %s", m.String(), base)
		}
		if err := os.MkdirAll(host, 0o755); err != nil {
			return "", err
		}
	}
	st, err := os.Stat(host)
	if err != nil {
		hostname, _ := os.Hostname()
		where := ""
		if hostname != "" {
			where = " (on " + hostname + ")"
		}
		return "", fmt.Errorf("mount %q: %s does not exist%s. A mount is a folder on the machine that runs the step; "+
			"a worker on another machine cannot see the server's disk, so either create it there or use the relative form, "+
			"which resolves under each machine's own data dir", m.String(), host, where)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("mount %q: %s is a file, not a folder", m.String(), host)
	}
	// The DENYLIST is applied to the RESOLVED path. A symlink called `stuff`
	// pointing at ~/.ssh is the obvious way past a check on the spelling, so
	// the spelling is not what is checked.
	resolved := resolveRoot(host)
	if err := forbiddenMount(resolved); err != nil {
		return "", fmt.Errorf("mount %q: %w", m.String(), err)
	}
	if err := withinMountRoots(resolved); err != nil {
		return "", fmt.Errorf("mount %q: %w", m.String(), err)
	}
	return resolved, nil
}

// forbiddenMount refuses the folders a workflow has no business attaching.
//
// This is an enumerated DENYLIST, and containment.go is right that enumerating
// what is forbidden is the weaker shape. It is used here anyway, because the
// alternative — an allowlist — would mean no mount works until an operator
// configures one, and a feature nobody can use is not safer, it is unused. The
// allowlist exists as the opt-in WFX_MOUNT_ROOTS below, and an operator who
// wants the strong rule sets it.
func forbiddenMount(abs string) error {
	clean := filepath.Clean(abs)
	slash := filepath.ToSlash(clean)
	lower := strings.ToLower(slash)

	// The OS temp directory is carved out FIRST. It is scratch space that any
	// process can already write, so refusing it protects nothing — and on macOS
	// it lives under /private/var, which the system-tree rule below would
	// otherwise reject, making every temp-folder mount (and every test) fail
	// for a reason that has nothing to do with safety.
	if tmp := resolveRoot(os.TempDir()); tmp != "" && !outside(tmp, clean) {
		return nil
	}

	// The filesystem root, or a bare drive. Mounting `/` is mounting every
	// other rule at once.
	if clean == string(filepath.Separator) || clean == "/" || isBareDrive(clean) {
		return fmt.Errorf("%s is the whole filesystem", abs)
	}
	// A home directory ITSELF — the keys, the shell history and every other
	// project are in there. A folder inside one is fine; that is the normal
	// case and the one this feature exists for.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if sameDir(clean, resolveRoot(home)) {
			return fmt.Errorf("%s is a home directory; mount the folder you need, not all of it", abs)
		}
	}
	// Anything holding credentials, wherever it lives. Checked SEGMENT BY
	// SEGMENT rather than as a prefix, so a mount of `~/projects/.ssh` or of a
	// parent that contains one is caught too.
	secretDirs := map[string]bool{
		".ssh": true, ".aws": true, ".gnupg": true, ".gpg": true, ".kube": true,
		".docker": true, ".npmrc": true, ".netrc": true, "keychains": true,
		".password-store": true, ".config": true, ".wfx-runner": true,
	}
	for _, seg := range strings.Split(lower, "/") {
		if secretDirs[seg] {
			return fmt.Errorf("%s is (or is inside) a credential directory", abs)
		}
	}
	// The system tree. A workflow that needs /etc does not need it through a
	// mount, and the containment guardrail already lets an ordinary command
	// reach an interpreter.
	for _, prefix := range []string{
		"/etc", "/var", "/usr", "/bin", "/sbin", "/boot", "/dev", "/proc", "/sys",
		"/system", "/library", "/private/etc", "/private/var",
	} {
		if lower == prefix || strings.HasPrefix(lower, prefix+"/") {
			return fmt.Errorf("%s is part of the system, not a workflow's data", abs)
		}
	}
	for _, prefix := range []string{"c:/windows", "c:/program files", "c:/programdata", "/c/windows"} {
		if lower == prefix || strings.HasPrefix(lower, prefix+"/") {
			return fmt.Errorf("%s is part of the system, not a workflow's data", abs)
		}
	}
	return nil
}

// withinMountRoots is the OPT-IN strong rule: with WFX_MOUNT_ROOTS set, a
// mount must be under one of the listed folders and the denylist above becomes
// a second line rather than the only one. An operator who runs other people's
// workflows should set it.
func withinMountRoots(abs string) error {
	raw := os.Getenv("WFX_MOUNT_ROOTS")
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var roots []string
	for _, r := range strings.Split(raw, string(os.PathListSeparator)) {
		if r = strings.TrimSpace(r); r != "" {
			roots = append(roots, resolveRoot(r))
			if !outside(resolveRoot(r), abs) {
				return nil
			}
		}
	}
	return fmt.Errorf("%s is not under any of WFX_MOUNT_ROOTS (%s)", abs, strings.Join(roots, ", "))
}

func isBareDrive(p string) bool {
	return len(p) == 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

func sameDir(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// resolveRoot is EvalSymlinks with the original as the fallback, which is what
// every comparison here wants: on macOS /var is a symlink to /private/var, and
// comparing a resolved path against an unresolved root is the bug containment.go
// has now met from both sides.
func resolveRoot(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}

// copyTree is the read-only mount. It follows no symlink OUT of the source: a
// link inside the folder is copied as a link only when it still points inside,
// and otherwise refused — a `data/escape -> /` would turn a read-only mount
// into a full filesystem copy and, worse, into a readable one.
func copyTree(src, dest string) error {
	srcRoot := resolveRoot(src)
	var bytesCopied int64
	var files int
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if d.Type()&fs.ModeSymlink != 0 {
			if outside(srcRoot, resolveRoot(p)) {
				return fmt.Errorf("%s is a symlink pointing outside the folder; a read-only mount copies what is in the folder, "+
					"and following that link would copy something else entirely", rel)
			}
			return nil // an inside link is already covered by the file it names
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil // a socket or a device is not data to copy
		}
		files++
		bytesCopied += info.Size()
		if bytesCopied > maxMountCopyBytes || files > maxMountCopyFiles {
			return fmt.Errorf("this read-only mount is over %d MB or %d files. Read-only means a COPY, because there is no "+
				"portable unprivileged read-only bind mount — for a folder this big, mount it `:rw` and take the risk knowingly, "+
				"or point the mount at the subfolder the workflow actually reads",
				maxMountCopyBytes>>20, maxMountCopyFiles)
		}
		return copyFile(p, target, info.Mode().Perm())
	})
}

func copyFile(src, dest string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// stageFiles writes the workflow's own files into the workspace, at the
// workspace ROOT — so `run: node run.js` works with the path the author wrote
// beside the YAML, and nothing has to be guessed or prefixed.
//
// A sidecar NEVER lands inside a mount: CheckFiles refuses that at load time,
// and it is checked again here because this function also runs on a worker,
// against files that arrived over the wire.
func stageFiles(workspace string, files []workflow.File, mounts []workflow.Mount) error {
	if len(files) == 0 {
		return nil
	}
	if err := workflow.CheckFiles("workflow files", files, mounts); err != nil {
		return err
	}
	root := resolveRoot(workspace)
	for _, f := range files {
		dest := filepath.Join(workspace, filepath.FromSlash(filepath.Clean(filepath.FromSlash(f.Path))))
		if outside(root, dest) {
			return fmt.Errorf("workflow file %q escapes the workspace", f.Path)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		// An existing file is REPLACED, not merged: the workflow's copy is the
		// authority for its own files, and a half-staged workspace where one
		// script is this version and another is last run's is worse than either.
		// It cannot clobber a mount — that was refused above.
		mode := fs.FileMode(f.Mode).Perm()
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(dest, f.Body, mode); err != nil {
			return err
		}
	}
	return nil
}

// Attach is the WORKER'S entry point: the same attaching and staging the
// server does, against this machine's disk and this machine's data dir.
//
// It is exported because a worker is a separate binary, and it is the SAME
// function so that a mount cannot mean one thing on the server and another on
// a build box — the failure mode every "and also implement it on the worker"
// eventually produces.
// Attach takes sourceDir for a `./` mount. A worker passes "" — it has no copy
// of the workflow's own directory — and a `./` mount is then refused by name
// rather than resolved into something else.
func Attach(workspace, dataDir, sourceDir string, mounts []workflow.Mount, files []workflow.File) (roots []string, env map[string]string, err error) {
	_, roots, env, err = attachMounts(workspace, dataDir, sourceDir, mounts)
	if err != nil {
		return nil, nil, err
	}
	if err := stageFiles(workspace, files, mounts); err != nil {
		return nil, nil, err
	}
	return roots, env, nil
}

// checkMountHosts applies the host denylist to what an author is SAVING,
// against this machine's disk.
//
// It deliberately does NOT require the folder to exist: a workflow may be
// authored here and run on a worker, and refusing a path this box has not got
// would make the platform the wrong authority. What it refuses is what is
// forbidden ANYWHERE — the filesystem root, a home directory, a credential
// directory, the system tree — which does not depend on the folder being here.
func (e *Engine) checkMountHosts(mounts []workflow.Mount) error {
	for _, m := range mounts {
		host := filepath.FromSlash(m.Host)
		if !filepath.IsAbs(host) {
			continue // relative: it lands under the data dir by construction
		}
		if err := forbiddenMount(resolveRoot(host)); err != nil {
			return fmt.Errorf("mount %q: %w", m.String(), err)
		}
		if err := withinMountRoots(resolveRoot(host)); err != nil {
			return fmt.Errorf("mount %q: %w", m.String(), err)
		}
	}
	return nil
}

// ---- per-run state ----

// mountState is what one run's `mount:` block became on this machine.
type mountState struct {
	// Roots are the folders outside the workspace the run's agents may reach —
	// the writable mounts, and only those.
	Roots []string
	// Env is WFX_MOUNT_<AT> for each mount, so a step names the variable
	// instead of hardcoding a path.
	Env map[string]string
	// Spec and Files are what the workflow DECLARED, kept so a step placed on
	// a worker can be sent them — the worker attaches the same folders against
	// its own disk and stages the same files itself.
	Spec  []workflow.Mount
	Files []workflow.File
}

// prepareAttachments attaches the workflow's mounts and stages its own files
// into the run's workspace. It runs once per resume, before any step.
func (e *Engine) prepareAttachments(ctx context.Context, runID uuid.UUID, def *workflow.Definition, workdir string) error {
	// Mounts go in FIRST and the workflow's own files second, so the ordering
	// matches the rule: a sidecar may never land inside a mount, and if one
	// ever did it is refused rather than quietly written over somebody's data.
	list, roots, env, err := attachMounts(workdir, e.cfg.WorkDir, def.SourceDir(), def.Mount)
	if err != nil {
		return err
	}
	if err := stageFiles(workdir, def.Files, def.Mount); err != nil {
		return err
	}
	e.mu.Lock()
	if e.attached == nil {
		e.attached = map[uuid.UUID]mountState{}
	}
	e.attached[runID] = mountState{Roots: roots, Env: env, Spec: def.Mount, Files: def.Files}
	e.mu.Unlock()

	for _, a := range list {
		how := "read-only (copied in)"
		if !a.Spec.ReadOnly {
			how = "writable (linked)"
		}
		e.emit(ctx, runID, "", "log", map[string]any{
			"text": fmt.Sprintf("mount %s → %s, %s", a.Host, a.Spec.At, how),
		})
	}
	if len(def.Files) > 0 {
		e.emit(ctx, runID, "", "log", map[string]any{
			"text": fmt.Sprintf("staged %d file(s) shipped with the workflow into the workspace", len(def.Files)),
		})
	}
	return nil
}

func (e *Engine) releaseAttachments(runID uuid.UUID) {
	e.mu.Lock()
	delete(e.attached, runID)
	e.mu.Unlock()
}

// mountRoots is what this run's agents may reach outside their workspace.
func (e *Engine) mountRoots(runID uuid.UUID) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.attached[runID].Roots
}

// mountEnv is the WFX_MOUNT_* block for this run.
func (e *Engine) mountEnv(runID uuid.UUID) map[string]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.attached[runID].Env
}

// attachmentsOf is what this run's workflow declared, for a step being packed
// to run somewhere else.
func (e *Engine) attachmentsOf(runID uuid.UUID) ([]workflow.Mount, []workflow.File) {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.attached[runID]
	return st.Spec, st.Files
}
