// Package authoring provides checkout-independent JSON templates and optional
// compilation of explicitly trusted local source definitions.
package authoring

import (
	"embed"
	"fmt"
)

//go:embed templates/*.json
var templates embed.FS

// Template returns a version-matched Plan IR document, without executing code.
func Template(name string) ([]byte, error) {
	switch name {
	case "echo", "task":
		return templates.ReadFile("templates/" + name + ".json")
	default:
		return nil, fmt.Errorf("unknown template %q (available: echo, task)", name)
	}
}
