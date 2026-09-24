package store

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/lib/pq"
)

// The store speaks ONE set of queries to two engines.
//
// The queries are written in Postgres dialect — that is the deployment target
// and the migrations that have already been applied are Postgres — and a
// dialect rewrites the handful of things SQLite spells differently on the way
// out. Only three statements genuinely differ (a LATERAL join, an array
// membership test, an array unnest); those carry a second spelling, and
// everything else is one query. A second store implementation would have been
// two of every query, drifting apart from the first patch onwards.
type dialect struct{ name string }

const (
	driverPostgres = "postgres"
	driverSQLite   = "sqlite"
)

func (d dialect) isSQLite() bool { return d.name == driverSQLite }

// pgCasts are the Postgres type annotations SQLite has no use for. They are
// removed rather than translated: SQLite is dynamically typed, so the cast was
// only ever there to tell Postgres what an untyped parameter was.
var pgCasts = strings.NewReplacer(
	"::jsonb", "",
	"::text", "",
	"::bytea", "",
	"::boolean", "",
)

// q rewrites one Postgres statement for the engine in hand.
//
// The placeholder rewrite is $N → ?N, not $N → ?, deliberately: several
// statements bind $1 last (an UPDATE's WHERE clause), and bare ? would rebind
// by position of appearance and silently write the wrong column. SQLite's
// numbered parameters keep the index the query states.
func (d dialect) q(sql string) string {
	if !d.isSQLite() {
		return sql
	}
	sql = pgCasts.Replace(sql)
	sql = strings.ReplaceAll(sql, "now()", "CURRENT_TIMESTAMP")
	var b strings.Builder
	for i := 0; i < len(sql); i++ {
		if sql[i] == '$' && i+1 < len(sql) && sql[i+1] >= '0' && sql[i+1] <= '9' {
			b.WriteByte('?')
			continue
		}
		b.WriteByte(sql[i])
	}
	return b.String()
}

// jsonArg passes a JSON document as a parameter. It goes as a string, never as
// []byte: Postgres reads an untyped string parameter into a jsonb column, while
// []byte would be offered as bytea and rejected.
func jsonArg(j json.RawMessage) any {
	if len(j) == 0 {
		return nil
	}
	return string(j)
}

// rawJSON scans a json/jsonb column into a json.RawMessage, which is not itself
// a sql.Scanner. Postgres hands back []byte, SQLite hands back a string.
type rawJSON struct{ dst *json.RawMessage }

// A column holding nothing — NULL, ”, or blanks — becomes a NIL RawMessage,
// never an empty non-nil one. The difference is not cosmetic: json.RawMessage
// encodes nil as `null` and an empty non-nil slice as a marshal ERROR
// ("unexpected end of JSON input"), which would take a whole API response down
// over one blank column. SQLite is where it bites, because the driver returns a
// text column as a Go string and []byte("") is not nil.
func (r rawJSON) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*r.dst = nil
	case []byte:
		*r.dst = json.RawMessage(append([]byte(nil), v...))
	case string:
		*r.dst = json.RawMessage(v)
	}
	if len(bytes.TrimSpace(*r.dst)) == 0 {
		*r.dst = nil
	}
	return nil
}

// labels crosses the one place the two schemas really differ: Postgres holds a
// worker's labels in a text[], SQLite in a JSON array. Both are read and
// written through this pair, so no caller knows which.
func (d dialect) labelsArg(l []string) any {
	if l == nil {
		l = []string{}
	}
	if d.isSQLite() {
		b, _ := json.Marshal(l)
		return string(b)
	}
	return pq.Array(l)
}

type labelsScan struct {
	d   dialect
	dst *[]string
}

func (s labelsScan) Scan(src any) error {
	if !s.d.isSQLite() {
		return (*pq.StringArray)(s.dst).Scan(src)
	}
	var b []byte
	switch v := src.(type) {
	case nil:
		*s.dst = []string{}
		return nil
	case []byte:
		b = v
	case string:
		b = []byte(v)
	}
	out := []string{}
	if err := json.Unmarshal(b, &out); err != nil {
		return err
	}
	*s.dst = out
	return nil
}
