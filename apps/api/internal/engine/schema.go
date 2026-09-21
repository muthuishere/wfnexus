package engine

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// printer is required by jsonschema's LocalizedString — passing nil panics.
var printer = message.NewPrinter(language.English)

// compileSchema compiles a JSON schema given as a Go map.
func compileSchema(name string, schema map[string]any) (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	if err := c.AddResource(name, schema); err != nil {
		return nil, err
	}
	return c.Compile(name)
}

// validateJSON validates an already-decoded value; the error message is written
// for the MODEL to read (it is fed back as the tool result), so it is terse and lists every failure.
func validateJSON(s *jsonschema.Schema, v any) error {
	err := s.Validate(v)
	if err == nil {
		return nil
	}
	if ve, ok := err.(*jsonschema.ValidationError); ok {
		var lines []string
		for _, c := range flatten(ve) {
			loc := strings.Join(c.InstanceLocation, "/")
			if loc == "" {
				loc = "(root)"
			}
			lines = append(lines, fmt.Sprintf("- %s: %s", loc, c.ErrorKind.LocalizedString(printer)))
		}
		return fmt.Errorf("output does not match the required schema:\n%s", strings.Join(lines, "\n"))
	}
	return err
}

func flatten(ve *jsonschema.ValidationError) []*jsonschema.ValidationError {
	if len(ve.Causes) == 0 {
		return []*jsonschema.ValidationError{ve}
	}
	var out []*jsonschema.ValidationError
	for _, c := range ve.Causes {
		out = append(out, flatten(c)...)
	}
	return out
}

// mustJSON marshals or returns "{}".
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}
