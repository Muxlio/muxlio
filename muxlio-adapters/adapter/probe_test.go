package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
)

func TestInstalledLoggedOutRunnerAndSecretDiagnostics(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "runner")
	script := `#!/bin/sh
if [ "$1" = --version ]; then echo 1.2.3; exit 0; fi
echo secret-token-value >&2
exit 1
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	checks := RunnerChecks(binary, "login", "status")
	if !checks[0].Passed || !checks[1].Passed || checks[2].Passed || checks[2].Name != contract.ProbeAuthentication {
		t.Fatal(checks)
	}
	raw, _ := json.Marshal(checks)
	if strings.Contains(string(raw), "secret-token-value") {
		t.Fatal("credential leaked")
	}
}

func TestVersionAndLoggedOutSuccessAreNotEvidence(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "runner")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho 'Not logged in'\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if CommandCheck(contract.ProbeVersion, binary, "--version").Passed {
		t.Fatal("version output was not verified")
	}
	if CommandCheck(contract.ProbeAuthentication, binary, "status").Passed {
		t.Fatal("logged-out success exit accepted")
	}
}

func TestCommandCheckRejectsIncompleteEvidence(t *testing.T) {
	for _, category := range []string{contract.ProbeVersion, contract.ProbeAuthentication} {
		for name, output := range map[string]string{"empty": "", "whitespace": "   ", "oversized": strings.Repeat("1.2.3 logged in ", 2000)} {
			t.Run(category+"/"+name, func(t *testing.T) {
				binary := filepath.Join(t.TempDir(), "runner")
				if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s' '"+output+"'\n"), 0700); err != nil {
					t.Fatal(err)
				}
				if CommandCheck(category, binary, "status").Passed {
					t.Fatal("incomplete evidence accepted")
				}
			})
		}
	}
}

func TestAuthenticationRejectsJSONWhitespace(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "runner")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s' '{\"loggedIn\" : false}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if CommandCheck(contract.ProbeAuthentication, binary, "status").Passed {
		t.Fatal("logged-out JSON accepted")
	}
}
