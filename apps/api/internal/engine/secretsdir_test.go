package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// On a worker (no work dir) a step's sealed values go to a file in a 0700
// directory of its own, removed with it; leftovers of a process that is gone
// are swept, a live process's are kept.
func TestAJobsSecretsGetTheirOwnDirectoryAndStaleOnesAreSwept(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes")
	}
	t.Setenv("TMPDIR", t.TempDir())
	e := &Engine{}
	prefix, _, drop, err := e.secretEnvFile(uuid.New(), "s", map[string]string{"API_TOKEN": "value-123456"}, nil)
	if err != nil || prefix == "" {
		t.Fatalf("%q %v", prefix, err)
	}
	path := strings.Trim(strings.TrimSuffix(strings.TrimPrefix(prefix, ". "), " && "), "'")
	dir := filepath.Dir(path)
	if filepath.Dir(dir) != secretsBase() || !strings.HasPrefix(filepath.Base(dir), "job-"+strconv.Itoa(os.Getpid())+"-") {
		t.Fatalf("dir = %s", dir)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v %v", st, err)
	}
	drop()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("the job's directory outlived it")
	}

	dead := filepath.Join(secretsBase(), "job-999999-abc")
	live := filepath.Join(secretsBase(), "job-"+strconv.Itoa(os.Getpid())+"-abc")
	for _, d := range []string{dead, live} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(d, "step.env"), []byte("export K=v"), 0o600)
	}
	if n := SweepStaleSecrets(); n != 1 {
		t.Fatalf("swept %d", n)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatal("a dead process's secrets were kept")
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatal("a live process's secrets were swept")
	}
}
