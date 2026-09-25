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
type loadOptions struct{ remote RemoteResolver }

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
