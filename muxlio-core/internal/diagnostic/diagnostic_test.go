package diagnostic

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBoundedRedactedDiagnostic(t *testing.T) {
	t.Setenv("TEST_API_TOKEN", "environment-credential")
	d := New("MUXLIO_TEST", "runtime", "Bearer bearer-secret token=inline-secret sk-123456789 environment-credential", "repair")
	d.Actual = map[string]any{"password": "private-value", "nested": []any{"api_key=another-secret"}}
	d.Causes = make([]Diagnostic, 150)
	for i := range d.Causes {
		d.Causes[i] = New("CAUSE", "runtime", strings.Repeat("x", 10000), "")
	}
	b, err := JSON(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"bearer-secret", "inline-secret", "sk-123456789", "environment-credential", "private-value", "another-secret"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	var got Diagnostic
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if (len(got.Causes) == 0 || len(got.Causes) > MaxIssues || len(b) > 64*1024) || len(got.Causes[0].Message) > MaxString+32 {
		t.Fatal("unbounded causes")
	}
	first := d.ID
	d.Message = "new prose"
	d.AttemptID = "attempt-2"
	d.Identify()
	if d.ID != first {
		t.Fatal("unstable ID")
	}
}

func TestKnownSecretsInNestedPayload(t *testing.T) {
	b := WithSecrets(map[string]any{"actual": map[string]any{"value": "private runtime value"}}, map[string]string{"credential": "private runtime value"})
	if strings.Contains(string(b), "private runtime value") || !json.Valid(b) {
		t.Fatalf("unsafe payload: %s", b)
	}
}

func TestJSONPreservesNumbersAndBoundsNestedData(t *testing.T) {
	b, err := JSON(map[string]any{"actual": json.Number("9007199254740993")})
	if err != nil || !strings.Contains(string(b), "9007199254740993") {
		t.Fatalf("lost number precision: %s %v", b, err)
	}
	values := make([]any, 100)
	for i := range values {
		values[i] = strings.Repeat("x", 10000)
	}
	b, err = JSON(map[string]any{"code": "MUXLIO_TEST", "message": "repairable", "actual": values})
	if err != nil || len(b) > 64*1024 || !json.Valid(b) || !strings.Contains(string(b), "MUXLIO_TEST") {
		t.Fatalf("unbounded payload: bytes=%d err=%v", len(b), err)
	}
}

func TestJSONBoundsEscapesKeysAndNumbers(t *testing.T) {
	values := make([]any, MaxIssues)
	for i := range values {
		values[i] = map[string]any{strings.Repeat("<", 10000): strings.Repeat("\x00", 10000)}
	}
	for _, value := range []any{
		map[string]any{"code": "MUXLIO_TEST", "actual": values},
		map[string]any{"actual": json.Number(strings.Repeat("1", MaxJSON*2))},
	} {
		b, err := JSON(value)
		if err != nil || !json.Valid(b) || len(b) > MaxJSON {
			t.Fatalf("unbounded JSON: bytes=%d err=%v", len(b), err)
		}
	}
}

func TestKnownSecretsBeforeTruncationAndInKeys(t *testing.T) {
	secret := strings.Repeat("private-credential", MaxString)
	d := New("MUXLIO_TEST", "runtime", secret, "repair")
	d.Causes = []Diagnostic{New("CAUSE", "runtime", secret, "")}
	d.Actual = map[string]any{secret: secret, "api-key": "structured-credential"}
	b := WithSecrets(d, map[string]string{"key": secret})
	if !json.Valid(b) || strings.Contains(string(b), "private-credential") || strings.Contains(string(b), "structured-credential") {
		t.Fatalf("secret survived sanitization: %.200s", b)
	}
}

func TestRedactUnquotedMultiWordCredentials(t *testing.T) {
	got := Redact(`password=first secret second`)
	for _, secret := range []string{"first", "second"} {
		if strings.Contains(got, secret) {
			t.Fatalf("unquoted credential leaked %q: %s", secret, got)
		}
	}
}

func TestRedactQuotedCredentials(t *testing.T) {
	for _, input := range []string{`password="first secret second"`, `password="first \"secret\" second"`, `token='first secret second'`, `HTTPS://user:private-value@example.com/path`} {
		got := Redact(input)
		for _, secret := range []string{"first", "second", "private-value"} {
			if strings.Contains(got, secret) {
				t.Errorf("credential leaked: %s", got)
			}
		}
	}
}

func TestDiagnosticCollectionRoundTripAtBudget(t *testing.T) {
	for size := 100; size <= MaxString; size += 17 {
		ds := make([]Diagnostic, MaxIssues)
		for i := range ds {
			ds[i] = New("MUXLIO_TEST", "runtime", strings.Repeat("x", size), "repair")
			ds[i].Source = &Source{Pointer: "/subgraphs/root/nodes/0", Line: 1, Column: 1}
			ds[i].Expected = strings.Repeat("x", size)
		}
		b, err := JSON(ds)
		var got []Diagnostic
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("size=%d: %v", size, err)
		}
	}
}
