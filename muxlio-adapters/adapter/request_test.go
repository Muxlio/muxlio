package adapter

import (
	"os"
	"strings"
	"testing"
)

func TestReadRequestRequiresOneObject(t *testing.T) {
	for _, raw := range []string{`null`, `{broken`, `{} {}`, `{} trailing`, `[]`} {
		t.Run(raw, func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "stdin")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if _, err := file.WriteString(raw); err != nil {
				t.Fatal(err)
			}
			if _, err := file.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			previous := os.Stdin
			os.Stdin = file
			t.Cleanup(func() { os.Stdin = previous; responseSecrets = nil })
			if _, err := ReadRequest(); err == nil || !strings.Contains(err.Error(), "adapter:") {
				t.Fatalf("invalid request accepted: %v", err)
			}
		})
	}
}
