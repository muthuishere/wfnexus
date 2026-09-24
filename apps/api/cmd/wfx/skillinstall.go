package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// `wfx install --skills` — put this platform's agent skills where YOUR agents
// will find them.
//
// Explicit, like `playwright install`, and for the same reason: a tool that
// writes into another tool's configuration behind your back is a tool nobody
// can audit. It prints what it would do, does exactly that, and says where.
//
// It copies files. It does not register anything, phone anywhere, or modify a
// config — so undoing it is `rm -rf` on a directory this command names.

// skillRoots are the GLOBAL directories agents read skills from. Both are
// written, not the first that happens to exist: one machine runs several agents
// and they do not share a root, so installing into one of them silently leaves
// the others without the skill.
func skillRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".claude", "skills"),
		filepath.Join(home, ".agents", "skills"),
	}
}

// installCmd is the front door for putting things on this machine. Today that
// is the skills; the flag is there so adding the next one does not need a new
// verb.
func installCmd(args []string) error {
	if hasFlag(args, "--skills") {
		return skillInstall(args)
	}
	if hasFlag(args, "--list") {
		return skillList()
	}
	return fmt.Errorf("usage: wfx install --skills [name…] [--to DIR] [--force]\n" +
		"       wfx install --list            what this platform ships")
}

// skillList shows what would be installed, and where.
func skillList() error {
	src, err := skillSourceDir()
	if err != nil {
		return err
	}
	names, err := localSkillNames(src)
	if err != nil {
		return err
	}
	fmt.Printf("skills shipped with this platform (%s)\n\n", src)
	for _, n := range names {
		fmt.Printf("  %-20s %s\n", n, firstLine(skillDescription(filepath.Join(src, n, "SKILL.md"))))
	}
	fmt.Printf("\ninstall all of them into your agents:  wfx install --skills\n")
	fmt.Printf("they go to:\n")
	for _, r := range skillRoots() {
		fmt.Printf("  %s\n", r)
	}
	return nil
}

func skillInstall(args []string) error {
	src, err := skillSourceDir()
	if err != nil {
		return err
	}
	all, err := localSkillNames(src)
	if err != nil {
		return err
	}

	to := flagOf(args, "--to", "")
	force := hasFlag(args, "--force")
	// ALL of them by default. These skills are how the platform's own workflows
	// are written; installing one of them and leaving the rest gives an agent
	// half a vocabulary, and the missing half is invisible until a workflow
	// names a skill that is not there.
	wanted := positional(args)
	if len(wanted) == 0 || (len(wanted) == 1 && wanted[0] == "all") {
		wanted = all
	}
	for _, w := range wanted {
		if !containsStr(all, w) {
			return fmt.Errorf("no skill named %q here — `wfx skill list` shows what there is", w)
		}
	}

	targets := skillRoots()
	if to != "" {
		targets = []string{to}
	}
	if len(targets) == 0 {
		return fmt.Errorf("could not work out where your agents keep skills — pass --to DIR")
	}

	for _, root := range targets {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return err
		}
		fmt.Printf("%s\n", root)
		var wrote, skipped int
		for _, name := range wanted {
			dest := filepath.Join(root, name)
			if _, err := os.Stat(dest); err == nil && !force {
				skipped++
				continue
			}
			n, err := copyTree(filepath.Join(src, name), dest)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			wrote++
			_ = n
		}
		fmt.Printf("  %d installed", wrote)
		if skipped > 0 {
			fmt.Printf(", %d already there (--force to overwrite)", skipped)
		}
		fmt.Println()
	}

	fmt.Printf("\n%d skills: %s\n", len(wanted), strings.Join(wanted, ", "))
	fmt.Printf("Your agents pick these up on their next session.\n")
	fmt.Printf("Remove with:  rm -rf %s\n", filepath.Join(targets[0], "<name>"))
	return nil
}

// skillSourceDir is where this platform's own skills live.
func skillSourceDir() (string, error) {
	if v := os.Getenv("WFX_SKILLS_DIR"); v != "" {
		return v, nil
	}
	// Beside the binary's repository when run from a checkout; otherwise the
	// installed location. Both are tried rather than assumed.
	for _, c := range []string{"skills", filepath.Join("..", "..", "skills")} {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return filepath.Abs(c)
		}
	}
	if root := os.Getenv("WFX_ROOT"); root != "" {
		return filepath.Join(root, "skills"), nil
	}
	return "", fmt.Errorf("cannot find this platform's skills — set WFX_SKILLS_DIR")
}

func localSkillNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "SKILL.md")); err == nil {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// skillDescription reads the frontmatter description, for the listing.
func skillDescription(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "description:") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "description:")), `"`)
		}
	}
	return ""
}

// copyTree copies a skill directory, returning how many files landed.
func copyTree(src, dest string) (int, error) {
	var n int
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		info, err := d.Info()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		defer out.Close()
		if _, err := io.Copy(out, in); err != nil {
			return err
		}
		n++
		return nil
	})
	return n, err
}

// positional drops flags and their values, leaving the names.
func positional(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "--") {
			if args[i] == "--to" {
				i++
			}
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
