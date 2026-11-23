package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// THE CUSTOMER API (ADR 0033): an issuer account (ADR 0031) lists, reads,
// validates, saves and runs workflows of ITS tenant.
//
//   - It is OFF: customerAPI is false and nothing in configuration sets it.
//     Every route answers 404 and does nothing until it is switched on, which
//     waits until write steps always pause for approval (#146, #148).
//   - The caller is an issuer account. A service principal, a person signed
//     in here and an anonymous loopback caller are refused.
//   - The tenant comes only from the token, through the operator's link.
//     Another tenant's workflow answers exactly as an absent one.
//   - A workflow is saved only into a plain-directory project of the tenant,
//     and only if every step is safe without an approval gate it cannot
//     rely on yet (customerWorkflowGuard). Connections stay operator-only,
//     and a customer workflow names no mcp server in this change.

// customerAPIForTest is how a test switches the customer API on. Nothing
// outside _test.go files assigns it.
var customerAPIForTest bool

func (s *Server) customerRoutes(r chi.Router) {
	r.Get("/svc/workflows", s.customerOnly(s.customerListWorkflows))
	r.Post("/svc/workflows", s.customerOnly(s.customerSaveWorkflow))
	r.Post("/svc/workflows/validate", s.customerOnly(s.customerValidateWorkflow))
	r.Get("/svc/workflows/{name}", s.customerOnly(s.customerGetWorkflow))
}

// customerOnly is the gate in front of every customer route: switched on,
// and an issuer account with a tenant. Off answers 404 before anything else.
func (s *Server) customerOnly(h func(http.ResponseWriter, *http.Request, *Subject)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.customerAPI {
			notFound(w)
			return
		}
		sub, ok := SubjectFrom(r.Context())
		if !ok || sub.Kind != SubjectAccount || sub.Tenant == "" {
			notFound(w)
			return
		}
		h(w, r, sub)
	}
}

// tenantWorkflow returns the definition key if the workflow exists and its
// project is in the tenant; otherwise "" (the caller answers 404).
func (s *Server) tenantWorkflow(r *http.Request, tenant, name string) (string, bool) {
	if s.eng.Definitions()[name] == nil {
		return "", false
	}
	tn, err := s.store.ProjectTenant(r.Context(), s.eng.ProjectFor(name))
	if err != nil || tn != tenant {
		return "", false
	}
	return name, true
}

func (s *Server) customerListWorkflows(w http.ResponseWriter, r *http.Request, sub *Subject) {
	out := []map[string]any{}
	names := make([]string, 0)
	for name := range s.eng.Definitions() {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := s.tenantWorkflow(r, sub.Tenant, name); ok {
			d := s.eng.Definitions()[name]
			out = append(out, map[string]any{"name": name, "project": s.eng.ProjectFor(name), "description": d.Description})
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) customerGetWorkflow(w http.ResponseWriter, r *http.Request, sub *Subject) {
	name, ok := s.tenantWorkflow(r, sub.Tenant, urlName(r, "name"))
	if !ok {
		notFound(w)
		return
	}
	writeJSON(w, 200, s.eng.Definitions()[name])
}

type customerSaveBody struct {
	Project    string          `json:"project"`
	Definition json.RawMessage `json:"definition"`
}

// decodeCustomerDefinition reads the body, checks the project is the
// tenant's and the definition passes the guard. It answers itself on error.
func (s *Server) decodeCustomerDefinition(w http.ResponseWriter, r *http.Request, sub *Subject) (string, *workflow.Definition, bool) {
	var b customerSaveBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Project == "" || len(b.Definition) == 0 {
		writeErr(w, 400, errors.New("project and definition are required"))
		return "", nil, false
	}
	// The project must be BOUND to this tenant. An unbound project is the
	// default tenant's, never a customer's.
	tn, err := s.store.ProjectTenant(r.Context(), b.Project)
	if err != nil || tn != sub.Tenant {
		notFound(w)
		return "", nil, false
	}
	def, err := workflow.DecodeDefinition(b.Definition)
	if err != nil {
		writeErr(w, 400, err)
		return "", nil, false
	}
	if err := workflow.ValidName(def.Name); err != nil {
		writeErr(w, 400, err)
		return "", nil, false
	}
	if err := customerWorkflowGuard(def); err != nil {
		writeErr(w, 400, err)
		return "", nil, false
	}
	return b.Project, def, true
}

func (s *Server) customerValidateWorkflow(w http.ResponseWriter, r *http.Request, sub *Subject) {
	_, def, ok := s.decodeCustomerDefinition(w, r, sub)
	if !ok {
		return
	}
	if err := s.eng.CheckSave(def); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) customerSaveWorkflow(w http.ResponseWriter, r *http.Request, sub *Subject) {
	project, def, ok := s.decodeCustomerDefinition(w, r, sub)
	if !ok {
		return
	}
	// A git-backed project would turn a save into a branch and a pull
	// request on its remote; a customer save never reaches outside.
	if t, err := s.eng.TargetForSave(r.Context(), project, def.Name); err != nil || t.Git {
		writeErr(w, http.StatusConflict, errors.New("this project cannot take a workflow from the customer API"))
		return
	}
	if _, err := s.eng.SaveWorkflowIn(project, def); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "project": project, "name": def.Name})
}

// customerWorkflowGuard refuses anything a customer workflow could use to
// act without the approval gate the engine does not yet guarantee (#146),
// or to reach what belongs to the operator. Allowed: agent steps with no tools
// (prompt, model, schema, budget, control flow). Everything that writes, runs
// code, reaches a worker, a mount, the env store, a module, an mcp server,
// another workflow or a channel is refused.
func customerWorkflowGuard(d *workflow.Definition) error {
	if err := guardDefinition(d); err != nil {
		return err
	}
	if !triggersEmpty(d.On) {
		return errors.New("triggers are not available to customer workflows: start runs through the API")
	}
	return guardSteps(d.Steps)
}

// customerRunGuard is customerWorkflowGuard without the triggers check: what
// a run started by a person may not use. An organization's guarded people
// (orgGuardRun) are held to it when they run, dispatch or dry-run a workflow
// already on disk.
func customerRunGuard(d *workflow.Definition) error {
	if err := guardDefinition(d); err != nil {
		return err
	}
	return guardSteps(d.Steps)
}

// guardDefinition is the workflow-level half of the guard.
func guardDefinition(d *workflow.Definition) error {
	switch {
	case len(d.Mount) > 0:
		return errors.New("mount is not available to customer workflows")
	case len(d.Files) > 0:
		return errors.New("files are not available to customer workflows")
	case len(d.Jobs) > 0:
		return errors.New("jobs are not available to customer workflows")
	case len(d.Uses) > 0 || len(d.RemotePins) > 0:
		return errors.New("uses is not available to customer workflows")
	case len(d.Env) > 0:
		return errors.New("env is not available to customer workflows")
	case d.RunsOn != "":
		return errors.New("runs-on is not available to customer workflows")
	}
	return nil
}

// triggersEmpty: no schedule, no inbound hook, no message trigger and no
// workflow_call. A manual dispatch (what the API's run is) is allowed.
func triggersEmpty(t workflow.Triggers) bool {
	return len(t.Schedule) == 0 && t.RepositoryDispatch == nil && len(t.Message) == 0 && !t.Call
}

func guardSteps(steps []workflow.Step) error {
	for _, st := range steps {
		bad := ""
		switch {
		case st.Run != "" || st.Shell != "":
			bad = "run"
		case st.Uses != "" || st.Module != "":
			bad = "uses"
		case st.Start != nil:
			bad = "start"
		case st.Send != nil:
			bad = "send"
		case len(st.Tools) > 0:
			bad = "tools"
		case len(st.Skills) > 0:
			bad = "skills"
		case len(st.Team) > 0:
			bad = "team"
		case len(st.Env) > 0:
			bad = "env"
		case st.RunsOn != "":
			bad = "runs-on"
		case len(st.State) > 0:
			bad = "state"
		case st.Memory != nil:
			bad = "memory"
		}
		if bad != "" {
			return fmt.Errorf("step %q: %s is not available to customer workflows", st.ID, bad)
		}
		// No mcp at all yet: the loader validates mcp names against the
		// server's registry, so a tenant connection name would fail every
		// reload. Customer workflows reach connections in a later change.
		if len(st.MCP) > 0 {
			return fmt.Errorf("step %q: mcp is not available to customer workflows yet", st.ID)
		}
		if err := guardSteps(st.Steps); err != nil {
			return err
		}
	}
	return nil
}
