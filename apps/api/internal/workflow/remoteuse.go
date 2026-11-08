package workflow

// RemoteResolver turns a parsed remote reference into a task, and says what it
// pinned. It lives outside this package (internal/remoteuse) because fetching
// is git's job and the loader's job is expansion.
//
// A nil resolver means remote references are UNAVAILABLE, not permissive: a
// remote `use:` is then a load error. Absent, never a silent fall back to a
// local task of the same name.
type RemoteResolver interface {
	ResolveRemoteUse(ref Ref) (*Task, RemotePin, error)
}

// loadOptions is what a caller may vary about a load.
type loadOptions struct {
	remote RemoteResolver
	// modules are installed modules (module.go), the last fallback for a
	// `use:` name after local tasks and workflows.
	modules map[string]*Task
	// workflow resolves a `use:` naming a sibling workflow in the same
	// directory. found=false means no such workflow.
	workflow func(name string) (t *Task, found bool, err error)
	// callable resolves a `use:` against workflows loaded ELSEWHERE — what a
	// save or check of one workflow sees (Callables) — after the directory
	// being loaded has had its say.
	callable func(name string) *Definition
}

// Callables is an optional Catalog extension: the workflows already loaded.
// With it, saving or checking a workflow that `uses:` another one resolves the
// callee there instead of failing because the temporary round-trip directory
// holds only the one file.
type Callables interface {
	Callable(name string) *Definition
}

func callableOpts(cat Catalog) []LoadOption {
	c, ok := cat.(Callables)
	if !ok {
		return nil
	}
	return []LoadOption{func(o *loadOptions) { o.callable = c.Callable }}
}

// LoadOption configures LoadDirWithTasks / LoadSources without changing the
// signature every existing caller already passes.
type LoadOption func(*loadOptions)

// WithRemoteResolver wires remote `use:` resolution. Without it, a workflow
// whose every `use:` is a bare task name loads exactly as it always has — no
// git process, no network, no cache.
func WithRemoteResolver(r RemoteResolver) LoadOption {
	return func(o *loadOptions) { o.remote = r }
}

func newLoadOptions(opts []LoadOption) loadOptions {
	var o loadOptions
	for _, f := range opts {
		if f != nil {
			f(&o)
		}
	}
	return o
}
