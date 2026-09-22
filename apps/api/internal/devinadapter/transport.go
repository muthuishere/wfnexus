package devinadapter

import (
	"net/http"

	toolnexus "github.com/muthuishere/toolnexus/golang"
)

// Transport exposes the same model as an http.RoundTripper, for the seams that
// take a transport instead of a Generate — notably agents.Options.Transport,
// which is how the sub-agent runtime is pointed at a model.
//
// It is toolnexus's own in-process round tripper, exported by ADR 0024 /
// issue #95 and shipped in v0.19.0. Before that this file carried a
// hand-copied version of it: the
// request decode, the choices[0].message assembly, the finish_reason
// derivation, argument encoding, the usage block and the streaming refusal —
// about 90 lines shadowing an unexported upstream file, with nothing keeping
// the two in step. A sub-agent now provably runs the IDENTICAL wire assembly
// as a top-level client, because it is the same function.
func (a *Adapter) Transport() http.RoundTripper {
	return toolnexus.InProcessTransport(a.Generate)
}

// AgentsLLM returns the LLM options the sub-agent runtime needs beside
// Transport: the sentinel base URL is never dialled, and the key is never used.
func (a *Adapter) AgentsLLM() (baseURL, apiKey, model string) {
	model = a.opts.Model
	if model == "" {
		model = DefaultModelLabel
	}
	return "http://in-process.invalid/v1", "in-process", model
}
