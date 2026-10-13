package devinadapter

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DevinSkillsDir is where devin looks for project skills, relative to its
// working directory (`devin skills paths`): .devin/skills/<name>/SKILL.md.
const DevinSkillsDir = ".devin/skills"

// IsDevin reports whether a cli/acp registry entry runs the devin binary:
// the devin preset (or no preset and no command), or a command whose binary
// is devin.
func IsDevin(preset string, command []string) bool {
	if len(command) > 0 {
		return filepath.Base(command[0]) == "devin"
	}
	return preset == "" || preset == "devin"
}

// MaterializeSkills copies each named skill directory into
// <workdir>/.devin/skills/<name>, so devin loads it as a native project skill.
// An existing copy is replaced. The directory is added to the checkout's
// .git/info/exclude so a later `git add -A` never commits it.
func MaterializeSkills(workdir string, dirs map[string]string) error {
	if workdir == "" || len(dirs) == 0 {
		return nil
	}
	base := filepath.Join(workdir, DevinSkillsDir)
	for name, src := range dirs {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
			return fmt.Errorf("devin skills: bad skill name %q", name)
		}
		dst := filepath.Join(base, name)
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("devin skills: %w", err)
		}
		if err := copyTree(src, dst); err != nil {
			return fmt.Errorf("devin skills: copy %s: %w", name, err)
		}
	}
	excludeFromGit(workdir, "/"+DevinSkillsDir+"/")
	return nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil // symlinks and devices are not part of a skill
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, info.Mode().Perm())
	})
}

// excludeFromGit appends line to <workdir>/.git/info/exclude when workdir is a
// plain git checkout and the line is not there yet. Best effort.
func excludeFromGit(workdir, line string) {
	gitDir := filepath.Join(workdir, ".git")
	if st, err := os.Stat(gitDir); err != nil || !st.IsDir() {
		return
	}
	path := filepath.Join(gitDir, "info", "exclude")
	cur, _ := os.ReadFile(path)
	for _, l := range strings.Split(string(cur), "\n") {
		if strings.TrimSpace(l) == line {
			return
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if len(cur) > 0 && !strings.HasSuffix(string(cur), "\n") {
		_, _ = f.WriteString("\n")
	}
	_, _ = f.WriteString(line + "\n")
}
