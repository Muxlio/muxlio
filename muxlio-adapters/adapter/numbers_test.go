package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRequestAndCollectedExactNumbers(t *testing.T) {
	for _, value := range []string{`9007199254740993`, `1e400`, `7`, `0`, `7.0`, `70e-1`, `"0"`, `{"values":[9007199254740993,1e400,7,0,"0"]}`} {
		t.Run(value, func(t *testing.T) {
			dir := t.TempDir()
			f, err := os.CreateTemp(dir, "stdin")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err := f.WriteString(`{"inputs":{"value":` + value + `}}`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			old := os.Stdin
			os.Stdin = f
			defer func() { os.Stdin = old; responseSecrets = nil }()
			req, err := ReadRequest()
			if err != nil {
				t.Error(err)
			} else {
				got, _ := json.Marshal(req.Inputs["value"])
				if string(got) != value {
					t.Errorf("request=%s want %s", got, value)
				}
			}
			for _, suffix := range []string{"", " {}", " trailing"} {
				if err := os.WriteFile(filepath.Join(dir, "value"), []byte(value+suffix), 0600); err != nil {
					t.Fatal(err)
				}
				out, missing, errs := CollectOutputs(&Request{WorkDir: dir, Outputs: map[string]OutputDecl{"value": {}}})
				if suffix != "" {
					if errs["value"] == "" {
						t.Error("accepted trailing JSON")
					}
					continue
				}
				got, _ := json.Marshal(out["value"])
				if string(got) != value || len(missing) > 0 || len(errs) > 0 {
					t.Errorf("collected=%s errs=%v want %s", got, errs, value)
				}
			}
		})
	}
}

func TestRequestRejectsTrailingDocuments(t *testing.T) {
	for _, suffix := range []string{" {}", " trailing", " 1e400"} {
		t.Run(suffix, func(t *testing.T) {
			f, err := os.CreateTemp(t.TempDir(), "stdin")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err = f.WriteString(`{"inputs":{"value":1e400}}` + suffix); err != nil {
				t.Fatal(err)
			}
			if _, err = f.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			old := os.Stdin
			os.Stdin = f
			defer func() { os.Stdin = old; responseSecrets = nil }()
			if _, err = ReadRequest(); err == nil {
				t.Fatal("accepted trailing request document")
			}
		})
	}
}
