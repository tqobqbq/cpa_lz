package managementasset

import (
	"fmt"
)

import _ "embed"

//go:embed embed/management.html
var embeddedManagementHTML []byte

// EmbeddedHTML returns a copy of the embedded management panel HTML.
func EmbeddedHTML() ([]byte, error) {
	if len(embeddedManagementHTML) == 0 {
		return nil, fmt.Errorf("embedded management.html is empty")
	}
	out := make([]byte, len(embeddedManagementHTML))
	copy(out, embeddedManagementHTML)
	return out, nil
}
