package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestConnectionsAreTenantScoped(t *testing.T) {
	bothDialects(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		sfx := uuid.NewString()[:8]
		ta, tb := "ca-"+sfx, "cb-"+sfx
		for _, id := range []string{ta, tb} {
			if _, err := st.CreateTenant(ctx, id, ""); err != nil {
				t.Fatal(err)
			}
		}
		c := &Connection{TenantID: ta, Name: "crm", Endpoint: "https://t.example.test", HeaderScheme: "Bearer", Tools: []string{"b", "a", "a"}}
		if err := st.CreateConnection(ctx, c, []byte("sealed-bytes")); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateConnection(ctx, &Connection{TenantID: ta, Name: "crm", Endpoint: "https://x.example.test"}, nil); !errors.Is(err, ErrConnectionExists) {
			t.Fatalf("duplicate: %v", err)
		}
		// The same name in another tenant is a different connection.
		if err := st.CreateConnection(ctx, &Connection{TenantID: tb, Name: "crm", Endpoint: "https://b.example.test"}, nil); err != nil {
			t.Fatal(err)
		}
		got, err := st.ConnectionInTenant(ctx, ta, "crm")
		if err != nil || got.Endpoint != "https://t.example.test" || strings.Join(got.Tools, ",") != "a,b" || string(got.SealedCredential()) != "sealed-bytes" {
			t.Fatalf("read: %+v %v", got, err)
		}
		gb, _ := st.ConnectionInTenant(ctx, tb, "crm")
		if gb.Endpoint != "https://b.example.test" || gb.HasCredential {
			t.Fatalf("tenant b sees: %+v", gb)
		}
		// The sealed bytes never serialise.
		raw, _ := json.Marshal(got)
		if strings.Contains(string(raw), "sealed-bytes") {
			t.Fatalf("credential in JSON: %s", raw)
		}
		list, _ := st.ListConnections(ctx, ta)
		if len(list) != 1 || list[0].SealedCredential() != nil {
			t.Fatalf("list carries the credential: %+v", list)
		}
		if err := st.SetConnectionTools(ctx, tb, "nope", []string{"x"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("tools on a missing connection: %v", err)
		}
		if err := st.SetConnectionTools(ctx, ta, "crm", []string{"z"}); err != nil {
			t.Fatal(err)
		}
		if gb, _ := st.ConnectionInTenant(ctx, tb, "crm"); len(gb.Tools) != 0 {
			t.Fatalf("tools leaked to tenant b: %v", gb.Tools)
		}
		if err := st.DeleteConnection(ctx, tb, "crm"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ConnectionInTenant(ctx, ta, "crm"); err != nil {
			t.Fatalf("deleting b's removed a's: %v", err)
		}
		// RunTenant is fail-closed like the reads.
		if err := st.BindProject(ctx, ta, "rp-"+sfx); err != nil {
			t.Fatal(err)
		}
		r, _ := st.CreateRun(ctx, "rp-"+sfx, "wf", json.RawMessage(`{}`))
		if tn, err := st.RunTenant(ctx, r.ID); err != nil || tn != ta {
			t.Fatalf("run tenant %q %v", tn, err)
		}
		if _, err := st.db.ExecContext(ctx, st.d.q(`UPDATE workflow_runs SET tenant_id=$1 WHERE id=$2`), tb, r.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.RunTenant(ctx, r.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("stale run tenant: %v", err)
		}
	})
}
