package main

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

// The boot log names the database but never prints its password (#172): a
// container's log is collected and kept by whatever runs it.
func TestTheBootLogNeverPrintsTheDatabasePassword(t *testing.T) {
	const pw = "s3cret-boot-pw-7731"
	for _, c := range []struct {
		name, dsn string
		keep      []string
	}{
		{"url", "postgres://wfx:" + pw + "@db.internal:5432/wfnexus?sslmode=require", []string{"db.internal:5432", "/wfnexus", "wfx"}},
		{"url with an escaped password", "postgres://wfx:" + pw + "%40x@db.internal/wfnexus", []string{"db.internal", "/wfnexus"}},
		{"key/value", "host=db.internal port=5432 user=wfx password=" + pw + " dbname=wfnexus", []string{"host=db.internal", "dbname=wfnexus", "password=xxxxx"}},
		{"key/value quoted", "host=db.internal password='" + pw + " x' dbname=wfnexus", []string{"host=db.internal", "dbname=wfnexus", "password=xxxxx"}},
	} {
		var buf bytes.Buffer
		log.SetOutput(&buf)
		logStorage("postgres", c.dsn)
		log.SetOutput(os.Stderr)
		out := buf.String()
		if strings.Contains(out, pw) {
			t.Errorf("%s: the boot log printed the password: %q", c.name, out)
		}
		for _, k := range c.keep {
			if !strings.Contains(out, k) {
				t.Errorf("%s: the boot log lost %q: %q", c.name, k, out)
			}
		}
	}
	// A sqlite path has no password and is printed as it is.
	var buf bytes.Buffer
	log.SetOutput(&buf)
	logStorage("sqlite", "/data/wfnexus.db")
	log.SetOutput(os.Stderr)
	if !strings.Contains(buf.String(), "storage: sqlite /data/wfnexus.db") {
		t.Errorf("sqlite: %q", buf.String())
	}
}
