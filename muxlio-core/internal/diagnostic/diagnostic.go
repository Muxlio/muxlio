// Package diagnostic defines the provider-independent failure contract.
package diagnostic

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

const MaxIssues = 100
const MaxString = 2048
const MaxJSON = 64 * 1024

type Source struct {
	File    string `json:"file,omitempty"`
	Pointer string `json:"pointer,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
}

type Diagnostic struct {
	ID           string       `json:"id"`
	Code         string       `json:"code"`
	Severity     string       `json:"severity,omitempty"`
	Phase        string       `json:"phase,omitempty"`
	Subgraph     string       `json:"subgraph,omitempty"`
	NodeID       string       `json:"nodeId,omitempty"`
	RunID        string       `json:"runId,omitempty"`
	ScopeID      string       `json:"scopeId,omitempty"`
	AttemptID    string       `json:"attemptId,omitempty"`
	ValuePath    string       `json:"valuePath,omitempty"`
	Source       *Source      `json:"source,omitempty"`
	Expected     any          `json:"expected,omitempty"`
	Actual       any          `json:"actual,omitempty"`
	Requirements any          `json:"requirements,omitempty"`
	Causes       []Diagnostic `json:"causes,omitempty"`
	Message      string       `json:"message"`
	Fix          string       `json:"fix,omitempty"`
}

func (d *Diagnostic) Error() string { return Redact(d.Code + ": " + d.Message) }

// Identify excludes prose and run/attempt IDs so reruns retain the same identity.
func (d *Diagnostic) Identify() {
	pointer := ""
	if d.Source != nil {
		pointer = d.Source.Pointer
		if pointer == "" {
			pointer = d.Source.File
		}
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{d.Code, d.Phase, d.Subgraph, d.NodeID, pointer}, "\x00")))
	d.ID = fmt.Sprintf("diag_%x", sum[:12])
}

func New(code, phase, message, fix string) Diagnostic {
	d := Diagnostic{Code: code, Phase: phase, Severity: "error", Message: message, Fix: fix}
	d.Identify()
	return d
}

// rawDiagnostic bypasses recursive marshaling so secrets are removed before
// any nested cause is truncated and one byte budget covers the entire chain.
func rawDiagnostic(d Diagnostic) any { return rawDiagnosticDepth(d, 0) }

func rawDiagnosticDepth(d Diagnostic, depth int) any {
	type plain Diagnostic
	if d.ID == "" {
		d.Identify()
	}
	causes := make([]any, 0)
	if depth < 4 {
		for i, cause := range d.Causes {
			if i == MaxIssues {
				break
			}
			causes = append(causes, rawDiagnosticDepth(cause, depth+1))
		}
	}
	return struct {
		plain
		Causes []any `json:"causes,omitempty"`
	}{plain(d), causes}
}

func (d Diagnostic) MarshalJSON() ([]byte, error) { return JSON(rawDiagnostic(d)) }

var bearerCredential = regexp.MustCompile(`(?i)((?:bearer|basic)\s+)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\s,"';&}]+)`)
var keyedCredential = regexp.MustCompile(`(?i)([a-z0-9_]*(?:token|password|secret|api[_-]?key)[a-z0-9_]*["']?\s*[:=]\s*)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^,;\n"&}]+)`)
var urlCredential = regexp.MustCompile(`(?i)(https?://)[^/\s:@]+:[^/\s@]+@`)
var token = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{8,}|gh[pousr]_[A-Za-z0-9_]+)\b`)

func Redact(s string) string {
	// Environment credentials are also removed when printed without their keys.
	var secrets []string
	for _, entry := range os.Environ() {
		k, v, _ := strings.Cut(entry, "=")
		key := strings.ToLower(k)
		if envSecretRedactable(v) && sensitive(key) {
			secrets = append(secrets, v)
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, v := range secrets {
		s = strings.ReplaceAll(s, v, "[REDACTED]")
	}
	s = keyedCredential.ReplaceAllString(s, "${1}[REDACTED]")
	s = bearerCredential.ReplaceAllString(s, "${1}[REDACTED]")
	s = token.ReplaceAllString(s, "[REDACTED]")
	s = urlCredential.ReplaceAllString(s, "${1}[REDACTED]@")
	if len(s) > MaxString {
		s = s[:MaxString] + "…[truncated]"
	}
	return s
}
// envSecretRedactable reports whether a process environment value is safe to
// replace globally in arbitrary error text. Short or dictionary-like values
// otherwise corrupt common network and permission diagnostics.
func envSecretRedactable(v string) bool {
	if len(v) < 8 {
		return false
	}
	if len(v) >= 16 {
		return true
	}
	for _, r := range v {
		if r < 'a' || r > 'z' {
			return true
		}
	}
	return false
}

func sensitive(k string) bool {
	k = strings.ReplaceAll(strings.ToLower(k), "-", "_")
	return strings.Contains(k, "token") || strings.Contains(k, "password") || strings.Contains(k, "secret") || strings.Contains(k, "api_key") || strings.Contains(k, "apikey") || k == "authorization"
}
func clean(v any, depth int) any {
	budget := MaxJSON - 1024
	return cleanBudget(v, depth, &budget)
}

func cleanBudget(v any, depth int, budget *int) any {
	*budget -= 16
	if depth > 12 || *budget <= 0 {
		return "[truncated]"
	}
	switch x := v.(type) {
	case string:
		safe := Redact(x)
		encoded, _ := json.Marshal(safe)
		for len(encoded) > *budget && len(safe) > 0 {
			safe = strings.ToValidUTF8(safe[:len(safe)/2], "")
			encoded, _ = json.Marshal(safe)
		}
		*budget -= len(encoded)
		return safe
	case []any:
		if len(x) > MaxIssues {
			x = x[:MaxIssues]
		}
		for i := range x {
			if *budget <= 32 {
				return x[:i]
			}
			x[i] = cleanBudget(x[i], depth+1, budget)
		}
		return x
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		priority := map[string]int{"code": 0, "id": 1, "severity": 2, "phase": 3, "message": 4, "fix": 5, "source": 6, "runId": 7, "scopeId": 8, "attemptId": 9, "nodeId": 10, "subgraph": 11, "executionSucceeded": 12, "valid": 13, "diagnostic": 14, "diagnostics": 15, "errors": 16, "details": 17, "warnings": 18, "validationFailure": 19, "expected": 20, "requirements": 21, "actual": 22, "valuePath": 23, "causes": 24}
		sort.Slice(keys, func(i, j int) bool {
			a, ok := priority[keys[i]]
			if !ok {
				a = 100
			}
			b, ok := priority[keys[j]]
			if !ok {
				b = 100
			}
			if a != b {
				return a < b
			}
			return keys[i] < keys[j]
		})
		out := map[string]any{}
		for i, k := range keys {
			if i >= MaxIssues || *budget <= 0 {
				out["truncated"] = true
				break
			}
			safeKey := Redact(k)
			encodedKey, _ := json.Marshal(safeKey)
			if len(encodedKey)+32 > *budget {
				out["truncated"] = true
				break
			}
			*budget -= len(encodedKey)
			if sensitive(k) {
				out[safeKey] = "[REDACTED]"
				*budget -= 12
			} else {
				out[safeKey] = cleanBudget(x[k], depth+1, budget)
			}
		}
		return out
	}
	encoded, _ := json.Marshal(v)
	if len(encoded) > *budget {
		return "[truncated]"
	}
	*budget -= len(encoded)
	return v
}

// JSON bounds and redacts every field, including nested causes and contracts.
func JSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var value any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err = dec.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(clean(value, 0))
}

// WithSecrets sanitizes a structured value before persistence or transport.
func WithSecrets(v any, secrets map[string]string) []byte {
	// Scrub known secrets before the diagnostic marshaler truncates strings.
	switch d := v.(type) {
	case Diagnostic:
		v = rawDiagnostic(d)
	case *Diagnostic:
		if d != nil {
			v = rawDiagnostic(*d)
		}
	}
	b, _ := json.Marshal(v)
	var value any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	_ = dec.Decode(&value)
	var scrub func(any) any
	scrub = func(v any) any {
		switch x := v.(type) {
		case string:
			values := make([]string, 0, len(secrets))
			for _, s := range secrets {
				if s != "" {
					values = append(values, s)
				}
			}
			sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
			for _, s := range values {
				x = strings.ReplaceAll(x, s, "[REDACTED]")
			}
			return x
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, v := range x {
				out[scrub(k).(string)] = scrub(v)
			}
			return out
		case []any:
			for i, v := range x {
				x[i] = scrub(v)
			}
		}
		return v
	}
	b, _ = json.Marshal(clean(scrub(value), 0))
	return b
}
