package engine

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// TENANT CONNECTIONS (ADR 0032).
//
// A run in a tenant other than the default one resolves its agent step's
// `mcp:` names to THAT tenant's connections, and only to them:
//
//   - the tenant is read from the stored run row, fail-closed (the row's
//     tenant and its project's current tenant must agree), never from the
//     workflow file or the input;
//   - a name with no connection in that tenant is an error; the server's own
//     mcp.json is NOT consulted, so a tenant's run can never reach the
//     operator's servers or their credentials;
//   - the step's YAML still decides WHICH connections (invariant 2); the
//     connection's tool list then narrows them. A tool not on the list is
//     never registered, so the agent can neither see nor call it. A
//     connection that lists no tools contributes none;
//   - the credential is opened in memory for the toolkit's request header
//     and registered with the run's redactions, so it is scrubbed from every
//     event like an env-store value;
//   - a step placed on a worker cannot use them (packAgentJob refuses): the
//     credential would ride in the job.
//
// It is OFF: tenantConnections is false and nothing in configuration sets it.
// OFF does not mean "fall back": a non-default tenant's step that names an
// mcp server is REFUSED, on the server and on a worker. The default tenant
// always resolves names from mcp.json, exactly as before.

// tenantConnectionsForTest is how a test switches tenant connections on.
// Nothing outside _test.go files assigns it.
var tenantConnectionsForTest bool

func (e *Engine) tenantConnectionsOn() bool { return tenantConnectionsForTest }

type stepRunKey struct{}

// withStepRun carries the run an agent step belongs to down to toolkit
// assembly, which is where its tenant is needed.
func withStepRun(ctx context.Context, runID uuid.UUID) context.Context {
	return context.WithValue(ctx, stepRunKey{}, runID)
}

func stepRunFrom(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(stepRunKey{}).(uuid.UUID)
	return id, ok && id != uuid.Nil
}

// connectionStore is what tenant resolution reads. The concrete store has
// it; a store that does not is refused rather than guessed around.
type connectionStore interface {
	RunTenant(ctx context.Context, id uuid.UUID) (string, error)
	ConnectionInTenant(ctx context.Context, tenant, name string) (*store.Connection, error)
}

// runTenant is the tenant whose connections a run uses, or "" for the
// default tenant (which uses mcp.json). Any doubt is an error, never "".
func (e *Engine) runTenant(ctx context.Context, runID uuid.UUID) (string, connectionStore, error) {
	cs, ok := e.store.(connectionStore)
	if !ok {
		// A store with no tenant tables (a test double) has only the
		// default tenant.
		return "", nil, nil
	}
	t, err := cs.RunTenant(ctx, runID)
	if err != nil {
		return "", nil, fmt.Errorf("tenant connections: the run's tenant cannot be resolved: %w", err)
	}
	if t == store.DefaultTenant {
		return "", cs, nil
	}
	return t, cs, nil
}

// splitStepMCP decides where a step's mcp names come from. It returns the
// names to resolve from mcp.json (as before), or, for a run in another
// tenant, a toolkit of that tenant's connections (the caller closes it) and
// the allow-listed tools to register.
func (e *Engine) splitStepMCP(ctx context.Context, label string, names []string) ([]string, *tn.Toolkit, []tn.Tool, error) {
	if len(names) == 0 {
		return names, nil, nil, nil
	}
	runID, ok := stepRunFrom(ctx)
	if !ok {
		return nil, nil, nil, fmt.Errorf("%s: tenant connections: no run to resolve a tenant from", label)
	}
	tenant, cs, err := e.runTenant(ctx, runID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", label, err)
	}
	if tenant == "" {
		return names, nil, nil, nil
	}
	if !e.tenantConnectionsOn() {
		return nil, nil, nil, fmt.Errorf("%s: this run's tenant cannot use mcp servers: tenant connections are switched off", label)
	}
	servers := map[string]any{}
	allowed := map[string]bool{}
	for _, name := range names {
		c, err := cs.ConnectionInTenant(ctx, tenant, name)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s: mcp %q is not a connection of this run's tenant", label, name)
		}
		if len(c.Tools) == 0 {
			continue
		}
		cfg := map[string]any{"type": "remote", "url": c.Endpoint}
		if sealed := c.SealedCredential(); len(sealed) > 0 {
			if e.secrets == nil {
				return nil, nil, nil, fmt.Errorf("%s: connection %q: the env store is unavailable, so its credential cannot be opened", label, name)
			}
			plain, err := e.secrets.Open(sealed)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("%s: connection %q: its credential cannot be opened", label, name)
			}
			value := plain
			if c.HeaderScheme != "" {
				value = c.HeaderScheme + " " + plain
			}
			cfg["headers"] = map[string]string{c.HeaderName: value}
			// Scrubbed from every event of this run, like an env-store value.
			key := "WFX_CONNECTION_" + tn.Sanitize(name)
			e.redactions.add(runID, map[string]string{key: plain, key + "_HEADER": value}, map[string]bool{key: true, key + "_HEADER": true})
		}
		only := map[string]bool{}
		for _, t := range c.Tools {
			only[t] = true
			allowed[tn.Sanitize(name)+"_"+tn.Sanitize(t)] = true
		}
		// toolnexus narrows the listing too; the filter below is the guard,
		// because an EMPTY toolnexus list means "every tool".
		cfg["tools"] = only
		servers[name] = cfg
	}
	if len(servers) == 0 {
		return nil, nil, nil, nil
	}
	tk, err := tn.CreateToolkit(ctx, tn.Options{McpConfig: map[string]any{"mcpServers": servers}})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: tenant connections: %w", label, err)
	}
	return nil, tk, allowedTools(tk.Tools(), allowed), nil
}

// allowedTools keeps the MCP tools whose (server-prefixed) name is allowed.
// It is the guard; the toolnexus per-server list is a second wall.
func allowedTools(all []tn.Tool, allowed map[string]bool) []tn.Tool {
	var keep []tn.Tool
	for _, t := range all {
		if t.Source == tn.SourceMCP && allowed[t.Name] {
			keep = append(keep, t)
		}
	}
	return keep
}

// refuseTenantConnectionsOnWorker is packAgentJob's guard.
func (e *Engine) refuseTenantConnectionsOnWorker(ctx context.Context, runID uuid.UUID, stepID string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	tenant, _, err := e.runTenant(ctx, runID)
	if err != nil {
		return fmt.Errorf("step %s: %w", stepID, err)
	}
	if tenant != "" {
		return fmt.Errorf("step %s: a non-default tenant's mcp servers are called from the server only, and only through its connections; this step cannot run on a worker", stepID)
	}
	return nil
}
