package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"

	"github.com/go-chi/chi/v5"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// A tenant's connections (ADR 0032): an operator records the endpoint, the
// credential the endpoint's owner issued for this tenant, and the tools this
// tenant's runs may call. The credential is sealed with the server's
// env-store key before it reaches the store; with no key it is refused. No
// route returns it.

func (s *Server) connectionRoutes(r chi.Router) {
	r.Get("/tenants/{tenant}/connections", s.listConnections)
	r.Post("/tenants/{tenant}/connections", s.createConnection)
	r.Put("/tenants/{tenant}/connections/{name}/tools", s.setConnectionTools)
	r.Delete("/tenants/{tenant}/connections/{name}", s.deleteConnection)
}

var headerNameRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// checkEndpoint: https, or http to a loopback host; no credentials in it.
func checkEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("endpoint must be an absolute URL without credentials")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		if ip := net.ParseIP(u.Hostname()); (ip != nil && ip.IsLoopback()) || u.Hostname() == "localhost" {
			return nil
		}
	}
	return errors.New("endpoint must be https (http only to a loopback host)")
}

func checkToolNames(tools []string) error {
	if len(tools) > maxDeclaredTools {
		return errors.New("too many tools")
	}
	for _, t := range tools {
		if !toolNameRe.MatchString(t) {
			return errors.New("tool names must be 1-128 of [A-Za-z0-9._:/-]")
		}
	}
	return nil
}

func (s *Server) listConnections(w http.ResponseWriter, r *http.Request) {
	t, ok := s.adminTenant(w, r)
	if !ok {
		return
	}
	cs, err := s.store.ListConnections(r.Context(), t)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, cs)
}

func (s *Server) createConnection(w http.ResponseWriter, r *http.Request) {
	t, ok := s.adminTenant(w, r)
	if !ok {
		return
	}
	var b struct {
		Name         string   `json:"name"`
		Endpoint     string   `json:"endpoint"`
		HeaderName   string   `json:"headerName"`
		HeaderScheme *string  `json:"headerScheme"`
		Credential   string   `json:"credential"`
		Tools        []string `json:"tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, 400, errors.New("invalid body"))
		return
	}
	if !principalNameRe.MatchString(b.Name) {
		writeErr(w, 400, errors.New("name must be 1-64 of [A-Za-z0-9._-]"))
		return
	}
	if err := checkEndpoint(b.Endpoint); err != nil {
		writeErr(w, 400, err)
		return
	}
	if b.HeaderName == "" {
		b.HeaderName = "Authorization"
	}
	if !headerNameRe.MatchString(b.HeaderName) {
		writeErr(w, 400, errors.New("headerName must be 1-64 of [A-Za-z0-9-]"))
		return
	}
	scheme := "Bearer"
	if b.HeaderScheme != nil {
		scheme = *b.HeaderScheme
	}
	if len(scheme) > 32 || !headerNameRe.MatchString(scheme) && scheme != "" {
		writeErr(w, 400, errors.New("headerScheme must be up to 32 of [A-Za-z0-9-]"))
		return
	}
	if err := checkToolNames(b.Tools); err != nil {
		writeErr(w, 400, err)
		return
	}
	var sealed []byte
	if b.Credential != "" {
		box := s.eng.Secrets()
		if box == nil {
			writeErr(w, http.StatusConflict, errors.New("the env store has no key, so a credential cannot be stored"))
			return
		}
		enc, err := box.Seal(b.Credential)
		if err != nil {
			writeErr(w, 500, errors.New("sealing failed"))
			return
		}
		sealed = enc
	}
	c := &store.Connection{TenantID: t, Name: b.Name, Kind: "mcp", Endpoint: b.Endpoint, HeaderName: b.HeaderName, HeaderScheme: scheme, Tools: b.Tools}
	if err := s.store.CreateConnection(r.Context(), c, sealed); err != nil {
		if errors.Is(err, store.ErrConnectionExists) {
			writeErr(w, 409, err)
			return
		}
		writeErr(w, 500, errors.New("create failed"))
		return
	}
	if c.Tools == nil {
		c.Tools = []string{}
	}
	writeJSON(w, 201, c)
}

func (s *Server) setConnectionTools(w http.ResponseWriter, r *http.Request) {
	t, ok := s.adminTenant(w, r)
	if !ok {
		return
	}
	var b struct {
		Tools []string `json:"tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, 400, errors.New("invalid body"))
		return
	}
	if err := checkToolNames(b.Tools); err != nil {
		writeErr(w, 400, err)
		return
	}
	name := urlName(r, "name")
	if err := s.store.SetConnectionTools(r.Context(), t, name, b.Tools); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFound(w)
			return
		}
		writeErr(w, 500, err)
		return
	}
	c, err := s.store.ConnectionInTenant(r.Context(), t, name)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, c)
}

func (s *Server) deleteConnection(w http.ResponseWriter, r *http.Request) {
	t, ok := s.adminTenant(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteConnection(r.Context(), t, urlName(r, "name")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFound(w)
			return
		}
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": true})
}
