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

// `wfx skill install` — put this platform's agent skills where YOUR agent will
// find them.
//
// Explicit, like `playwright install`, and for the same reason: a tool that
// writes into another tool's configuration behind your back is a tool nobody
// can audit. It prints what it would do, does exactly that, and says where.
//
// It copies files. It does not register anything, phone anywhere, or modify a
// config — so undoing it is `rm -rf` on a directory this command names.

// skillRoots are the directories agents read skills from, in the order they are
// offered. A root that does not exist is offered anyway: choosing it creates it.
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

func skillCmd(args []string) error {
	switch {
	case len(args) == 0 || args[0] == "list":
		return skillList()
	case args[0] == "install":
		return skillInstall(args[1:])
	}
	return fmt.Errorf("usage: wfx skill [list | install [name…] [--to DIR] [--force]]")
}

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
	fmt.Printf("\ninstall into your own agent:  wfx skill install workflow-author\n")
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
	wanted := positional(args)
	if len(wanted) == 0 {
		// The authoring skill is the one that makes this platform usable from
		// somebody else's agent, so it is the default rather than everything.
		wanted = []string{"workflow-author"}
	}
	if len(wanted) == 1 && wanted[0] == "all" {
		wanted = all
	}
	for _, w := range wanted {
		if !containsStr(all, w) {
			return fmt.Errorf("no skill named %q here — `wfx skill list` shows what there is", w)
		}
	}

	if to == "" {
		to = chooseRoot()
		if to == "" {
			return fmt.Errorf("could not work out where your agent keeps skills — pass --to DIR")
		}
	}
	if err := os.MkdirAll(to, 0o755); err != nil {
		return err
	}

	for _, name := range wanted {
		dest := filepath.Join(to, name)
		if _, err := os.Stat(dest); err == nil && !force {
			fmt.Printf("  %-20s already there — pass --force to overwrite\n", name)
			continue
		}
		n, err := copyTree(filepath.Join(src, name), dest)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		fmt.Printf("  %-20s → %s (%d files)\n", name, dest, n)
	}
	fmt.Printf("\nYour agent will pick these up on its next session.\n")
	fmt.Printf("Remove them with:  rm -rf %s\n", filepath.Join(to, "<name>"))
	return nil
}

// chooseRoot picks the first directory that already exists, so an install
// lands where an agent is actually configured rather than creating a second
// root it will never read.
func chooseRoot() string {
	roots := skillRoots()
	for _, r := range roots {
		if st, err := os.Stat(r); err == nil && st.IsDir() {
			return r
		}
	}
	if len(roots) > 0 {
		return roots[0]
	}
	return ""
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
