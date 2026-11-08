package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// DecodeJSON preserves arbitrary JSON numbers in interface fields while typed
// fields retain their declared types. Exactly one complete document is required.
// Callers reading streams remain responsible for their existing byte limits.
func DecodeJSON(raw []byte, target any) error {
	if !json.Valid(raw) {
		return fmt.Errorf("invalid JSON document")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(target)
}
