package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// hashLike mirrors internal/auth.HashToken. It is duplicated here rather than
// imported so that the store's test cannot be made to pass by a change to the
// auth package — the storage rule is asserted against the raw value.
func hashLike(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

func TestUsersRolesAndTokens(t *testing.T) {
	st := openSQLite(t)
	ctx := context.Background()

	if n, err := st.CountUsers(ctx); err != nil || n != 0 {
		t.Fatalf("a fresh store has %d users (err %v); the solo case must have none", n, err)
	}

	if err := st.UpsertRole(ctx, "admin", []string{"*"}); err != nil {
		t.Fatal(err)
	}
	if perms, err := st.Role(ctx, "admin"); err != nil || len(perms) != 1 || perms[0] != "*" {
		t.Fatalf("role round trip: %v %v", perms, err)
	}

	u := &User{Name: "muthu", Role: "admin"}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if u.ID == uuid.Nil || u.Created.IsZero() {
		t.Fatalf("user did not come back populated: %+v", u)
	}

	value := "wfx_" + strings.Repeat("ab", 24)
	tok, err := st.IssueUserToken(ctx, u.ID, hashLike(value), "cli", "")
	if err != nil {
		t.Fatal(err)
	}

	got, gotTok, err := st.UserByTokenHash(ctx, hashLike(value))
	if err != nil || got.Name != "muthu" || gotTok.ID != tok.ID {
		t.Fatalf("resolve by hash: %+v %+v %v", got, gotTok, err)
	}

	// The value itself must not resolve anything: only the hash is a key.
	if _, _, err := st.UserByTokenHash(ctx, value); err != ErrNotFound {
		t.Fatalf("the raw value resolved a subject: %v", err)
	}

	list, err := st.ListUserTokens(ctx, u.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list tokens: %v %v", list, err)
	}

	if err := st.RevokeUserTokenByHash(ctx, hashLike(value)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UserByTokenHash(ctx, hashLike(value)); err != ErrNotFound {
		t.Fatalf("a revoked token still authenticates: %v", err)
	}
}

// A user token and a worker token are separate rows in separate tables, so
// revoking every user token leaves a machine mid-job alone (ADR 0017).
func TestRevokingAUserTokenLeavesWorkersAlone(t *testing.T) {
	st := openSQLite(t)
	ctx := context.Background()

	w := &Worker{Name: "box-1", Labels: []string{"linux"}}
	if err := st.RegisterWorker(ctx, w, hashLike("wfx_worker")); err != nil {
		t.Fatal(err)
	}
	u := &User{Name: "muthu", Role: "admin"}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, err := st.IssueUserToken(ctx, u.ID, hashLike("wfx_user"), "", ""); err != nil {
		t.Fatal(err)
	}

	if err := st.RevokeUserTokenByHash(ctx, hashLike("wfx_user")); err != nil {
		t.Fatal(err)
	}
	if got, err := st.WorkerByToken(ctx, hashLike("wfx_worker")); err != nil || got.Name != "box-1" {
		t.Fatalf("the worker token stopped working: %+v %v", got, err)
	}
}

// [SEC-TEST] tasks 3.4 — after a token is issued, the VALUE appears in no
// column of any table. The scan is over every text-ish cell in the database,
// not over the columns we expect to hold it, because the point is to catch the
// column nobody thought of.
func TestNoTableHoldsTheTokenValue(t *testing.T) {
	st := openSQLite(t)
	ctx := context.Background()

	u := &User{Name: "muthu", Role: "admin"}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	value := "wfx_deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	if _, err := st.IssueUserToken(ctx, u.ID, hashLike(value), "cli", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateDeviceCode(ctx, hashLike("dev_"+value), "BCDF-GHJK", "wfx-cli", "laptop", "", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	for _, table := range dumpTables(t, st) {
		if strings.Contains(table.dump, value) {
			t.Fatalf("table %s holds the token value", table.name)
		}
	}
}

type tableDump struct{ name, dump string }

func dumpTables(t *testing.T, st *Store) []tableDump {
	t.Helper()
	ctx := context.Background()
	names, err := st.query(ctx, `SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for names.Next() {
		var n string
		if err := names.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	names.Close()

	var out []tableDump
	for _, name := range tables {
		rows, err := st.DB().QueryContext(ctx, `SELECT * FROM "`+name+`"`)
		if err != nil {
			continue
		}
		cols, _ := rows.Columns()
		var buf bytes.Buffer
		for rows.Next() {
			cells := make([]any, len(cols))
			for i := range cells {
				cells[i] = new(any)
			}
			if err := rows.Scan(cells...); err != nil {
				break
			}
			for _, c := range cells {
				buf.WriteString(stringify(*(c.(*any))))
				buf.WriteByte('\n')
			}
		}
		rows.Close()
		out = append(out, tableDump{name, buf.String()})
	}
	return out
}

func stringify(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	case string:
		return x
	default:
		return ""
	}
}
