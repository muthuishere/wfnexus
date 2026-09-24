package bundle

import (
	"bytes"
	"encoding/json"
)

// jsonUnmarshalStrict refuses a manifest with a field we do not know. A bundle
// published by a NEWER client carries meaning this server cannot honour, and
// quietly dropping it would run something other than what was published.
func jsonUnmarshalStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
