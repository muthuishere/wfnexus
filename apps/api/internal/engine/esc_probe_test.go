package engine

import (
	"testing"

	tn "github.com/muthuishere/toolnexus/golang"
)

func beforeTool(cmd string) tn.BeforeToolEvent {
	return tn.BeforeToolEvent{Name: "bash", Args: map[string]any{"command": cmd}}
}

func TestEscapeProbe(t *testing.T) {
	ws := t.TempDir()
	rail := containmentGuardrail(ws)
	probes := map[string]string{
		"env -C":           `env -C /etc ls`,
		"make -C":          `make -C /etc all`,
		"tar -C":           `tar -C /etc -cf x.tar .`,
		"find -execdir":    `find / -name secret -execdir cat {} \;`,
		"python chdir":     `python3 -c "import os; os.chdir('/etc'); print(open('passwd').read())"`,
		"absolute read":    `cat /etc/passwd`,
		"absolute write":   `echo pwned > /tmp/pwned-by-agent`,
		"absolute git":     `git --work-tree=/etc status`,
		"bash -c cd":       `bash -c 'cd /etc && ls'`,
		"subshell cd":      `(cd /etc && ls)`,
		"rsync to outside": `rsync -a . /tmp/exfil/`,
	}
	for name, cmd := range probes {
		got := rail(beforeTool(cmd))
		status := "ALLOWED  <-- escape"
		if got != "" {
			status = "denied"
		}
		t.Logf("%-18s %-10s %s", name, status, cmd)
	}
}
