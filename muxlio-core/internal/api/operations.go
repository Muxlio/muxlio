package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"github.com/muxlio/muxlio-core/internal/state"
)

func decodeOperationalRequest(w http.ResponseWriter, r *http.Request, value any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return err
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}

func registerOperations(mux *http.ServeMux, store state.Store, driver artifacts.Driver) {
	getStore := func(w http.ResponseWriter) (state.OperationsStore, bool) {
		s, ok := store.(state.OperationsStore)
		if !ok {
			writeError(w, 503, CodeStoreError, "operational storage unavailable")
		}
		return s, ok
	}
	mux.HandleFunc("/v1/automation/history", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writeError(w, 405, CodeMethodNotAllowed, "use GET")
			return
		}
		s, ok := getStore(w)
		if !ok {
			return
		}
		kind, id := r.URL.Query().Get("kind"), r.URL.Query().Get("id")
		if (kind != "schedules" && kind != "triggers" && kind != "subscriptions" && kind != "deadletters") || (kind != "deadletters" && id == "") {
			writeError(w, 400, CodeBadRequest, "choose an automation history")
			return
		}
		offset, limit := pageOptions(r)
		items, err := s.AutomationHistoryPage(kind, id, offset, limit+1)
		if err != nil {
			writeError(w, 500, CodeStoreError, "could not read automation history")
			return
		}
		safe := make([]map[string]any, 0, len(items))
		for _, raw := range items {
			var row map[string]any
			if json.Unmarshal(raw, &row) != nil {
				writeError(w, 500, CodeStoreError, "invalid stored delivery record")
				return
			}
			for _, field := range []string{"reason", "lastError"} {
				if value, ok := row[field].(string); ok {
					row[field] = diagnostic.Redact(value)
				}
			}
			safe = append(safe, row)
		}
		writeJSON(w, 200, pageResult(safe, offset, limit, "records"))
	})
	mux.HandleFunc("/v1/revisions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writeError(w, 405, CodeMethodNotAllowed, "use GET")
			return
		}
		s, ok := getStore(w)
		if !ok {
			return
		}
		runID := r.URL.Query().Get("runId")
		if runID == "" {
			writeError(w, 400, CodeMissingRunID, "runId is required")
			return
		}
		if _, err := runHeader(store, runID); err != nil {
			writeError(w, 404, CodeNotFound, "run not found")
			return
		}
		before, _ := strconv.Atoi(r.URL.Query().Get("before"))
		if before < 0 {
			before = 0
		}
		_, limit := pageOptions(r)
		items, err := s.ArtifactRevisionPage(runID, before, limit+1)
		if err != nil {
			writeError(w, 500, CodeStoreError, "could not read revisions")
			return
		}
		more := len(items) > limit
		if more {
			items = items[:limit]
		}
		next := before
		if len(items) > 0 {
			next = items[len(items)-1].Seq
		}
		writeJSON(w, 200, map[string]any{"revisions": items, "nextBefore": next, "hasMore": more})
	})
	mux.HandleFunc("/v1/automation", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			writeError(w, 405, CodeMethodNotAllowed, "use POST")
			return
		}
		s, ok := getStore(w)
		if !ok {
			return
		}
		var req struct {
			Kind    string `json:"kind"`
			ID      string `json:"id"`
			Enabled *bool  `json:"enabled"`
		}
		if decodeOperationalRequest(w, r, &req) != nil || req.ID == "" || req.Enabled == nil || (req.Kind != "schedules" && req.Kind != "triggers" && req.Kind != "subscriptions") {
			writeError(w, 400, CodeBadRequest, "choose an automation and enabled state")
			return
		}
		if err := s.SetAutomationEnabled(req.Kind, req.ID, *req.Enabled); err != nil {
			writeError(w, 404, CodeNotFound, "automation not found")
			return
		}
		writeJSON(w, 200, map[string]any{"updated": true})
	})
	mux.HandleFunc("/v1/operations", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writeError(w, 405, CodeMethodNotAllowed, "use GET")
			return
		}
		s, ok := getStore(w)
		if !ok {
			return
		}
		kind := r.URL.Query().Get("kind")
		switch kind {
		case "", "queue", "attention", "detached", "recovery", "runs":
		default:
			writeError(w, 400, CodeBadRequest, "unknown operational category")
			return
		}
		offset, limit := pageOptions(r)
		items, counts, err := s.OperationalPage(kind, offset, limit+1)
		if err != nil {
			writeError(w, 500, CodeStoreError, "could not read operations")
			return
		}
		for i := range items {
			items[i].Reason = diagnostic.Redact(items[i].Reason)
		}
		page := pageResult(items, offset, limit, "items")
		page["counts"] = counts
		writeJSON(w, 200, page)
	})
	mux.HandleFunc("/v1/retention", func(w http.ResponseWriter, r *http.Request) {
		s, ok := getStore(w)
		if !ok {
			return
		}
		switch r.Method {
		case "GET":
			pins, err := s.ListRetentionPins()
			if err != nil {
				writeError(w, 500, CodeStoreError, "could not read retention pins")
				return
			}
			offset, limit := pageOptions(r)
			total := len(pins)
			if offset > total {
				offset = total
			}
			page := pageResult(pins[offset:], offset, limit, "pins")
			page["total"] = total
			page["policy"] = state.DefaultRetention()
			writeJSON(w, 200, page)
		case "POST":
			var req struct {
				Kind          string          `json:"kind"`
				ID            string          `json:"id"`
				Pinned        *bool           `json:"pinned"`
				RetainedUntil json.RawMessage `json:"retainedUntil"`
			}
			if err := decodeOperationalRequest(w, r, &req); err != nil {
				writeError(w, 400, CodeBadRequest, "invalid retention request")
				return
			}
			if req.ID == "" || len(req.ID) > 512 || (req.Kind != "run" && req.Kind != "plan" && req.Kind != "artifact") || (req.Pinned == nil) == (req.RetainedUntil == nil) {
				writeError(w, 400, CodeBadRequest, "choose a valid target and exactly one pin or deadline operation")
				return
			}
			var err error
			if req.Pinned != nil {
				if req.Kind == "artifact" && *req.Pinned {
					if driver == nil {
						writeError(w, 503, CodeStoreError, "artifact storage unavailable")
						return
					}
					stream, _, getErr := driver.GetStream(r.Context(), req.ID)
					if getErr != nil {
						writeError(w, 404, CodeNotFound, "artifact unavailable")
						return
					}
					_ = stream.Close()
				}
				err = s.PinRetention(req.Kind, req.ID, *req.Pinned)
			} else {
				if req.Kind != "run" {
					writeError(w, 400, CodeBadRequest, "deadlines apply to runs")
					return
				}
				var until *time.Time
				if json.Unmarshal(req.RetainedUntil, &until) != nil {
					writeError(w, 400, CodeBadRequest, "deadline must be an RFC3339 timestamp or null")
					return
				}
				if until != nil && (until.Year() < 1970 || until.Year() > 9990) {
					writeError(w, 400, CodeBadRequest, "deadline outside supported range")
					return
				}
				err = s.SetRunRetention(req.ID, "", until)
			}
			if err != nil {
				writeError(w, 400, CodeBadRequest, "retention target unavailable or invalid")
				return
			}
			writeJSON(w, 200, map[string]any{"updated": true})
		default:
			writeError(w, 405, CodeMethodNotAllowed, "use GET or POST")
		}
	})
	reports := func(w http.ResponseWriter, r *http.Request) {
		s, ok := getStore(w)
		if !ok {
			return
		}
		if id := strings.TrimPrefix(r.URL.Path, "/v1/reports/"); r.URL.Path != "/v1/reports" {
			if r.Method != "GET" {
				writeError(w, 405, CodeMethodNotAllowed, "use GET")
				return
			}
			report, err := s.GetOperationalReport(id)
			if err != nil {
				writeError(w, 404, CodeNotFound, "report not found")
				return
			}
			writeJSON(w, 200, report)
			return
		}
		switch r.Method {
		case "GET":
			offset, limit := pageOptions(r)
			items, err := s.ListOperationalReports(offset, limit+1)
			if err != nil {
				writeError(w, 500, CodeStoreError, "could not list reports")
				return
			}
			writeJSON(w, 200, pageResult(items, offset, limit, "reports"))
		case "POST":
			var report state.OperationalReport
			var req struct {
				Kind     string          `json:"kind"`
				Name     string          `json:"name"`
				Document json.RawMessage `json:"document"`
			}
			if err := decodeOperationalRequest(w, r, &req); err != nil {
				writeError(w, 400, CodeBadRequest, "invalid report upload (maximum 8 MiB)")
				return
			}
			if len(strings.TrimSpace(req.Name)) == 0 || len(req.Name) > 200 {
				writeError(w, 400, CodeBadRequest, "report name must contain 1..200 characters")
				return
			}
			if err := validateOperationalReport(req.Kind, req.Document); err != nil {
				writeError(w, 400, CodeBadRequest, err.Error())
				return
			}
			var compact bytes.Buffer
			_ = json.Compact(&compact, req.Document)
			sum := sha256.Sum256(append([]byte(req.Kind+"\n"), compact.Bytes()...))
			report = state.OperationalReport{ID: hex.EncodeToString(sum[:]), Kind: req.Kind, Name: req.Name, ImportedAt: time.Now().UTC(), Document: compact.Bytes()}
			if err := s.SaveOperationalReport(report); err != nil {
				writeError(w, 500, CodeStoreError, "could not store report")
				return
			}
			saved, err := s.GetOperationalReport(report.ID)
			if err != nil {
				writeError(w, 500, CodeStoreError, "could not read imported report")
				return
			}
			saved.Document = nil
			writeJSON(w, 201, saved)
		default:
			writeError(w, 405, CodeMethodNotAllowed, "use GET or POST")
		}
	}
	mux.HandleFunc("/v1/reports", reports)
	mux.HandleFunc("/v1/reports/", reports)
}

// Imports are immutable evidence. They never start executions or load code.
func validateOperationalReport(kind string, raw json.RawMessage) error {
	var evidence any
	if json.Unmarshal(raw, &evidence) != nil {
		return fmt.Errorf("invalid report JSON")
	}
	if err := validateEvidenceMeasurements(evidence); err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil || doc == nil {
		return fmt.Errorf("report must be a JSON object")
	}
	switch kind {
	case "evaluation":
		var manifest struct {
			DatasetID string `json:"datasetId"`
			Versions  []struct {
				ID         string `json:"id"`
				PlanDigest string `json:"planDigest"`
			} `json:"versions"`
		}
		var results []struct {
			CaseID    string   `json:"caseId"`
			VersionID string   `json:"versionId"`
			Duration  *float64 `json:"durationMs"`
			Execution struct {
				State   string `json:"state"`
				Success *bool  `json:"success"`
			} `json:"execution"`
		}
		if json.Unmarshal(doc["manifest"], &manifest) != nil || manifest.DatasetID == "" || len(manifest.Versions) == 0 || json.Unmarshal(doc["results"], &results) != nil || results == nil || len(results) > 10000 {
			return fmt.Errorf("expected evaluation manifest, versions and up to 10000 results")
		}
		versions := map[string]bool{}
		for _, v := range manifest.Versions {
			if v.ID == "" || v.PlanDigest == "" || versions[v.ID] {
				return fmt.Errorf("invalid evaluation version")
			}
			versions[v.ID] = true
		}
		seen := map[string]bool{}
		for _, row := range results {
			key := row.VersionID + "\x00" + row.CaseID
			if row.CaseID == "" || !versions[row.VersionID] || row.Duration == nil || *row.Duration < 0 || row.Execution.Success == nil || row.Execution.State == "" || seen[key] {
				return fmt.Errorf("invalid or duplicate evaluation case result")
			}
			seen[key] = true
		}
	case "benchmark":
		var bundle struct {
			SchemaVersion int `json:"schemaVersion"`
			Manifest      struct {
				Suite         string   `json:"suite"`
				SchemaVersion int      `json:"schemaVersion"`
				Cases         []string `json:"cases"`
				Repetitions   int      `json:"repetitions"`
			} `json:"manifest"`
			Observations []struct {
				SchemaVersion int      `json:"schemaVersion"`
				Repetition    int      `json:"repetition"`
				Case          string   `json:"case"`
				Wall          *float64 `json:"wallTimeMs"`
				Correct       *bool    `json:"correct"`
			} `json:"observations"`
			Configurations []struct {
				Manifest struct {
					Suite string `json:"suite"`
				} `json:"manifest"`
				Cases []struct {
					Case      string `json:"case"`
					Planned   *int   `json:"planned"`
					Collected *int   `json:"collected"`
					Correct   *int   `json:"correct"`
				} `json:"cases"`
			} `json:"configurations"`
		}
		if json.Unmarshal(raw, &bundle) != nil {
			return fmt.Errorf("invalid benchmark document")
		}
		if bundle.Observations != nil {
			if bundle.Manifest.Suite != "muxlio-reference/v1" || bundle.Manifest.SchemaVersion != 1 || len(bundle.Observations) > 10000 || len(bundle.Manifest.Cases) == 0 || bundle.Manifest.Repetitions < 1 {
				return fmt.Errorf("unsupported benchmark manifest")
			}
			cases, seen := map[string]bool{}, map[string]bool{}
			for _, name := range bundle.Manifest.Cases {
				if name == "" || cases[name] {
					return fmt.Errorf("invalid benchmark cases")
				}
				cases[name] = true
			}
			for _, o := range bundle.Observations {
				key := fmt.Sprintf("%s/%d", o.Case, o.Repetition)
				if !cases[o.Case] || o.SchemaVersion != 1 || o.Repetition < 1 || o.Repetition > bundle.Manifest.Repetitions || seen[key] || o.Wall == nil || *o.Wall < 0 || o.Correct == nil {
					return fmt.Errorf("invalid benchmark observation")
				}
				seen[key] = true
			}
		} else if bundle.SchemaVersion == 1 && len(bundle.Configurations) > 0 && len(bundle.Configurations) <= 100 {
			for _, c := range bundle.Configurations {
				if c.Manifest.Suite != "muxlio-reference/v1" || len(c.Cases) == 0 || len(c.Cases) > 10000 {
					return fmt.Errorf("invalid benchmark configuration")
				}
				for _, row := range c.Cases {
					if row.Case == "" || row.Planned == nil || *row.Planned < 0 || row.Collected == nil || row.Correct == nil || *row.Collected < 0 || *row.Collected > *row.Planned || *row.Correct < 0 || *row.Correct > *row.Collected {
						return fmt.Errorf("invalid benchmark case summary")
					}
				}
			}
		} else {
			return fmt.Errorf("expected benchmark manifest and observations, or comparison configurations")
		}
	default:
		return fmt.Errorf("kind must be evaluation or benchmark")
	}
	return nil
}

// Unknown is a count of unavailable measurements. Zero measurements stay zero.
func validateEvidenceMeasurements(value any) error {
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			if err := validateEvidenceMeasurements(item); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, item := range v {
			if key == "usage" && item != nil {
				usage, ok := item.(map[string]any)
				if !ok {
					return fmt.Errorf("usage must be structured accounting")
				}
				for _, field := range []string{"tokens", "costUsd"} {
					if total, exists := usage[field]; exists && total != nil {
						row, ok := total.(map[string]any)
						if !ok {
							return fmt.Errorf("invalid usage total")
						}
						for _, metric := range []string{"observed", "estimated", "unknown"} {
							number, exists := row[metric]
							if !exists || number == nil {
								continue
							}
							n, ok := number.(float64)
							if !ok || n < 0 || math.IsInf(n, 0) || math.IsNaN(n) || (metric == "unknown" && math.Trunc(n) != n) {
								return fmt.Errorf("invalid usage measurement")
							}
						}
					}
				}
			}
			// Input/output JSON belongs to the experiment, not its measurement schema.
			if key != "input" && key != "output" && key != "outputs" && key != "configuration" && key != "plan" {
				if err := validateEvidenceMeasurements(item); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
