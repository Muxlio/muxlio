package sqlite

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/trigger"
)

func TestWebhookNumericGCRoots(t *testing.T) {
	for _, value := range []string{`7`, `0`, `7.0`, `-0`, `9007199254740993`, `1e400`, `{"values":[1e400,9007199254740993,0]}`, `{"%s":{"ref":"cas://%s","values":[1e400,9007199254740993,0]}}`} {
		t.Run(value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "webhook.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { s.Close() }()
			local, err := artifacts.NewLocal(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			keyRef, err := local.Put([]byte("map key"), "text/plain")
			if err != nil {
				t.Fatal(err)
			}
			stringRef, err := local.Put([]byte("string value"), "text/plain")
			if err != nil {
				t.Fatal(err)
			}
			orphan, err := local.Put([]byte("orphan"), "text/plain")
			if err != nil {
				t.Fatal(err)
			}
			hasRefs := strings.Contains(value, "%s")
			if hasRefs {
				value = fmt.Sprintf(value, keyRef.Digest, stringRef.Digest)
			}
			tr := trigger.Trigger{ID: "numbers", WorkflowDigest: "pin", Secret: "0123456789abcdef", Enabled: true, InputMapping: json.RawMessage(`"{{/n}}"`)}
			if err := s.CreateTrigger(tr); err != nil {
				t.Fatal(err)
			}
			mapped, err := tr.MapInput([]byte(`{"n":` + value + `}`))
			if err != nil {
				t.Fatal(err)
			}
			delivery, err := s.DeliverWebhook(trigger.WebhookDeliveryRequest{TriggerID: tr.ID, WorkflowDigest: "pin", WorkflowName: "numbers", PlanJSON: []byte(`{}`), Input: mapped})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SetRunState(delivery.RunID, "succeeded"); err != nil {
				t.Fatal(err)
			}
			// Keep the run's roots retained while orphan artifacts age out.
			if err := s.PinRetention("run", delivery.RunID, true); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			run, err := s.GetRun(delivery.RunID)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(run.Input)
			if string(got) != value {
				t.Fatalf("input=%s", got)
			}
			// Empty approval is preview only; no artifact or repository data is deleted.
			if _, err := s.CollectGarbage(local, state.DefaultRetention(), time.Now(), ""); err != nil {
				t.Fatal(err)
			}
			policy := state.DefaultRetention()
			policy.Artifacts = time.Hour
			report, err := s.CollectGarbage(local, policy, time.Now().Add(2*time.Hour), "")
			if err != nil {
				t.Fatalf("valid committed webhook input %s blocks GC preview: %v", value, err)
			}
			if report == nil {
				t.Fatal("missing GC preview")
			}
			candidates := map[string]bool{}
			for _, candidate := range report.Artifacts {
				candidates[candidate.Digest] = true
			}
			if !candidates[orphan.Digest] {
				t.Fatal("unrelated orphan omitted from GC preview")
			}
			if hasRefs && (candidates[keyRef.Digest] || candidates[stringRef.Digest]) {
				t.Fatal("numeric root lost artifact references")
			}
			if !local.Has(orphan.Digest) {
				t.Fatal("preview deleted artifact")
			}
		})
	}
}

func TestMarkJSONNumbersAndInvalidDocuments(t *testing.T) {
	for _, raw := range []string{`1e400`, `9007199254740993`, `0`, `{"values":[1e400,9007199254740993,0]}`} {
		if err := markJSON(raw, map[string]bool{}); err != nil {
			t.Errorf("valid root %s: %v", raw, err)
		}
	}
	for _, raw := range []string{`{`, `1e`, `01`, `1e400 null`, `{"n":1e400} {}`, `null trailing`} {
		if err := markJSON(raw, map[string]bool{}); err == nil {
			t.Errorf("accepted invalid document %s", raw)
		}
	}
}
