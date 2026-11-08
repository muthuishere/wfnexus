package main

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"

	"golang.org/x/term"
)

// `wfx user …` and `wfx role …` (ADR 0025): the admin pages, from a terminal.
// Every verb is a call to the same /api/users and /api/roles routes the UI
// uses, so authorization is the server's, never the CLI's.
//
// A password is never taken as an argument (it would land in shell history
// and `ps`): it is read from a terminal without echo, or from stdin with
// --password-stdin. A password the SERVER generates is printed once.

type apiUser struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	Project     string `json:"project"`
	Disabled    bool   `json:"disabled"`
	MustChange  bool   `json:"mustChange"`
	HasPassword bool   `json:"hasPassword"`
}

type apiRole struct {
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

const userUsage = `usage:
  wfx user ls
  wfx user add NAME [--role superadmin] [--project P] [--password-stdin]
  wfx user disable NAME [--enable]
  wfx user passwd [NAME] [--password-stdin]   NAME: an admin reset; none: your own
  wfx user role NAME superadmin|none     (project/workflow roles: wfx member add)`

func userCmd(args []string) error {
	if len(args) == 0 {
		args = []string{"ls"}
	}
	flags, pos := splitFlags(args[1:])
	switch args[0] {
	case "ls", "list":
		return userList()
	case "add":
		if len(pos) != 1 {
			return fmt.Errorf("%s", userUsage)
		}
		return userAdd(pos[0], flags["role"], flags["project"], flags["password-stdin"] != "")
	case "disable":
		if len(pos) != 1 {
			return fmt.Errorf("%s", userUsage)
		}
		return userUpdate(pos[0], func(u *apiUser) { u.Disabled = flags["enable"] == "" })
	case "role":
		if len(pos) != 2 {
			return fmt.Errorf("%s", userUsage)
		}
		// ADR 0028: superadmin or none; scoped roles are `wfx member add`.
		return userUpdate(pos[0], func(u *apiUser) { u.Role = strings.TrimPrefix(pos[1], "none") })
	case "passwd", "password":
		if len(pos) == 0 {
			return changeOwnPassword(flags["password-stdin"] != "")
		}
		return resetPassword(pos[0], flags["password-stdin"] != "")
	}
	return fmt.Errorf("%s", userUsage)
}

// splitFlags reads `--k v`, `--k=v` and bare boolean flags. Booleans come back
// as "true"; a value flag that is missing its value is left empty.
func splitFlags(args []string) (map[string]string, []string) {
	bools := map[string]bool{"password-stdin": true, "enable": true}
	flags := map[string]string{}
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			pos = append(pos, a)
			continue
		}
		k := strings.TrimPrefix(a, "--")
		if eq := strings.IndexByte(k, '='); eq >= 0 {
			flags[k[:eq]] = k[eq+1:]
			continue
		}
		if bools[k] {
			flags[k] = "true"
			continue
		}
		if i+1 < len(args) {
			flags[k] = args[i+1]
			i++
		}
	}
	return flags, pos
}

func userList() error {
	var users []apiUser
	if err := call("GET", "/api/users", nil, &users); err != nil {
		return err
	}
	fmt.Print(formatUsers(users))
	return nil
}

func formatUsers(users []apiUser) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-20s %-12s %-14s %-9s %s\n", "NAME", "ROLE", "PROJECT", "STATUS", "PASSWORD")
	for _, u := range users {
		status := "active"
		if u.Disabled {
			status = "disabled"
		}
		pw := "none"
		if u.HasPassword {
			pw = "set"
			if u.MustChange {
				pw = "must change"
			}
		}
		project := u.Project
		if project == "" {
			project = "*"
		}
		fmt.Fprintf(&b, "%-20s %-12s %-14s %-9s %s\n", u.Name, u.Role, project, status, pw)
	}
	return b.String()
}

func userAdd(name, role, project string, fromStdin bool) error {
	body := map[string]any{"name": name, "role": role, "project": project}
	// No credential comes back: a person signs in with the password.
	if err := call("POST", "/api/users", body, nil); err != nil {
		return err
	}
	fmt.Printf("created %s (%s)\n", name, role)
	return resetPassword(name, fromStdin)
}

// userUpdate is read-modify-write over PUT /api/users/{name}, which replaces
// every editable field — so the current values are sent back unchanged.
func userUpdate(name string, change func(*apiUser)) error {
	var users []apiUser
	if err := call("GET", "/api/users", nil, &users); err != nil {
		return err
	}
	for _, u := range users {
		if u.Name != name {
			continue
		}
		change(&u)
		body := map[string]any{"displayName": u.DisplayName, "role": u.Role, "project": u.Project, "disabled": u.Disabled}
		if err := call("PUT", "/api/users/"+url.PathEscape(name), body, nil); err != nil {
			return err
		}
		state := "active"
		if u.Disabled {
			state = "disabled"
		}
		fmt.Printf("%s: role %s, %s\n", name, u.Role, state)
		return nil
	}
	return fmt.Errorf("no user %q", name)
}

// resetPassword is an admin setting someone's password. Without
// --password-stdin the server generates one, which is printed once.
func resetPassword(name string, fromStdin bool) error {
	body := map[string]any{}
	if fromStdin {
		pw, err := readPasswordLine(os.Stdin)
		if err != nil {
			return err
		}
		body["password"] = pw
	}
	var out struct {
		Password string `json:"password"`
	}
	if err := call("POST", "/api/users/"+url.PathEscape(name)+"/password", body, &out); err != nil {
		return err
	}
	if out.Password != "" {
		fmt.Printf("one-time password for %s (shown once; they must change it at first sign-in):\n\n    %s\n\n", name, out.Password)
	} else {
		fmt.Printf("%s: password set; they must change it at first sign-in\n", name)
	}
	return nil
}

func changeOwnPassword(fromStdin bool) error {
	var cur, next string
	var err error
	if fromStdin {
		// Two lines: the current password, then the new one.
		rd := bufio.NewReader(os.Stdin)
		if cur, err = readPasswordLine(rd); err == nil {
			next, err = readPasswordLine(rd)
		}
	} else {
		if cur, err = promptHidden("current password: "); err == nil {
			if next, err = promptHidden("new password (12+ characters): "); err == nil {
				var again string
				if again, err = promptHidden("new password again: "); err == nil && again != next {
					err = fmt.Errorf("the two new passwords differ")
				}
			}
		}
	}
	if err != nil {
		return err
	}
	if err := call("POST", "/api/auth/password/change", map[string]string{"current": cur, "new": next}, nil); err != nil {
		return err
	}
	fmt.Println("password changed")
	return nil
}

func readPasswordLine(r io.Reader) (string, error) {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReader(r)
	}
	line, err := br.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("no password on stdin")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func promptHidden(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("not a terminal: use --password-stdin")
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	return string(b), err
}

const roleUsage = `usage:
  wfx role ls
  wfx role set NAME PERM[,PERM…] [PERM…]    replaces the role's permissions ("*" is everything)`

func roleCmd(args []string) error {
	if len(args) == 0 || args[0] == "ls" || args[0] == "list" {
		var roles []apiRole
		if err := call("GET", "/api/roles", nil, &roles); err != nil {
			return err
		}
		sort.Slice(roles, func(i, j int) bool { return roles[i].Name < roles[j].Name })
		for _, r := range roles {
			fmt.Printf("%-14s %s\n", r.Name, strings.Join(r.Permissions, ", "))
		}
		return nil
	}
	if args[0] == "set" && len(args) >= 3 {
		perms := parsePerms(args[2:])
		if err := call("PUT", "/api/roles/"+url.PathEscape(args[1]), map[string]any{"permissions": perms}, nil); err != nil {
			return err
		}
		fmt.Printf("%s: %s\n", args[1], strings.Join(perms, ", "))
		return nil
	}
	return fmt.Errorf("%s", roleUsage)
}

func parsePerms(args []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, a := range args {
		for _, p := range strings.Split(a, ",") {
			p = strings.TrimSpace(p)
			if p != "" && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}
