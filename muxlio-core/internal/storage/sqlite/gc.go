package sqlite

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/runtimefiles"
	"github.com/muxlio/muxlio-core/internal/state"
)

// GCReport is a deterministic preview. Apply requires its token and recalculates
// reachability; a changed candidate set requires another operator review.
type GCReport struct {
	ArtifactStorage string                            `json:"artifactStorage"`
	Artifacts       []artifacts.Candidate             `json:"artifacts"`
	Events          map[string]int                    `json:"events"`
	Logs            map[string]int                    `json:"stdoutStderr"`
	Plans           []string                          `json:"cachedPlans"`
	Token           string                            `json:"token"`
	Applied         bool                              `json:"applied"`
	Workspaces      []runtimefiles.WorkspaceCandidate `json:"workspaces,omitempty"`
}

// PinRetention pins a run, plan, or artifact and its transitive references.
func (s *Store) PinRetention(kind, id string, pinned bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if kind == "artifact" {
		if len(id) != 71 || !strings.HasPrefix(id, "sha256:") || strings.ToLower(id) != id {
			return artifacts.ErrInvalidDigest
		}
		if _, err := hex.DecodeString(id[7:]); err != nil {
			return artifacts.ErrInvalidDigest
		}
		if pinned {
			_, err := s.db.Exec(`INSERT OR IGNORE INTO retention_pins(kind,id) VALUES(?,?)`, kind, id)
			return err
		}
		_, err := s.db.Exec(`DELETE FROM retention_pins WHERE kind=? AND id=?`, kind, id)
		return err
	}
	table := "runs"
	if kind == "plan" {
		table = "workflow_versions"
	} else if kind != "run" {
		return fmt.Errorf("invalid pin kind")
	}
	column := "id"
	if kind == "plan" {
		column = "digest"
	}
	var found int
	if err := s.db.QueryRow("SELECT 1 FROM "+table+" WHERE "+column+"=?", id).Scan(&found); err != nil {
		return err
	}
	if pinned {
		_, err := s.db.Exec(`INSERT OR IGNORE INTO retention_pins(kind,id) VALUES(?,?)`, kind, id)
		return err
	}
	_, err := s.db.Exec(`DELETE FROM retention_pins WHERE kind=? AND id=?`, kind, id)
	return err
}

// SetRunRetention records an optional parent dependency for a fork and a
// retention deadline. Ancestors are protected while any descendant is retained.
// This is metadata only; execution and copying a fork remain the caller's job.
func (s *Store) SetRunRetention(id, parent string, until *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var found int
	if err := tx.QueryRow(`SELECT 1 FROM runs WHERE id=?`, id).Scan(&found); err != nil {
		return err
	}
	seen := map[string]bool{id: true}
	next := parent
	for next != "" {
		if seen[next] {
			return fmt.Errorf("retention parent cycle")
		}
		seen[next] = true
		if err := tx.QueryRow(`SELECT 1 FROM runs WHERE id=?`, next).Scan(&found); err != nil {
			return err
		}
		var ancestor sql.NullString
		err := tx.QueryRow(`SELECT parent_id FROM run_retention WHERE run_id=?`, next).Scan(&ancestor)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		next = ancestor.String
	}
	var deadline any
	if until != nil {
		deadline = rfc3339(*until)
	}
	var parentArg any
	if parent != "" {
		parentArg = parent
	}
	if _, err := tx.Exec(`INSERT INTO run_retention(run_id,parent_id,retained_until) VALUES(?,?,?) ON CONFLICT(run_id) DO UPDATE SET parent_id=COALESCE(excluded.parent_id,run_retention.parent_id),retained_until=excluded.retained_until`, id, parentArg, deadline); err != nil {
		return err
	}
	return tx.Commit()
}

// CollectGarbage must run with the runtime service.lock held exclusively, so
// uploads and execution cannot race the mark/sweep. It also holds a SQLite write
// transaction across preview validation and deletion to exclude other writers.
// Empty approval is always read-only. Zero durations disable their category.
func (s *Store) CollectGarbage(local *artifacts.Local, policy state.Retention, now time.Time, approval string, workspaceRoots ...string) (*GCReport, error) {
	return s.collectGarbage(local, policy, now, approval, false, workspaceRoots...)
}

// CollectIdleGarbage requires the caller's HTTP maintenance gate. It refuses
// cleanup while an engine or unfinished assignment could publish artifacts.
func (s *Store) CollectIdleGarbage(local *artifacts.Local, policy state.Retention, now time.Time, approval string, workspaceRoot string) (*GCReport, error) {
	return s.collectGarbage(local, policy, now, approval, true, workspaceRoot)
}

func (s *Store) collectGarbage(local *artifacts.Local, policy state.Retention, now time.Time, approval string, idleOnly bool, workspaceRoots ...string) (*GCReport, error) {
	if policy.EventHistory < 0 || policy.StdoutStderr < 0 || policy.Artifacts < 0 || policy.Caches < 0 || policy.Workspaces < 0 {
		return nil, fmt.Errorf("negative retention duration")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if idleOnly {
		var busy bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM runs WHERE state IN ('running','waiting')) OR EXISTS(SELECT 1 FROM assignments WHERE state IN ('active','uncertain'))`).Scan(&busy); err != nil {
			return nil, err
		}
		if busy {
			return nil, nil
		}
	}
	if _, err := tx.Exec(`UPDATE coordinator_meta SET value=value WHERE key='epoch'`); err != nil {
		return nil, err
	}
	report := &GCReport{ArtifactStorage: "local", Events: map[string]int{}, Logs: map[string]int{}, Plans: []string{}}
	type record struct {
		run    state.Run
		parent string
		raw    []string
	}
	runs := map[string]*record{}
	err = gcRows(tx, `SELECT r.id,r.state,r.updated_at,r.plan_digest,r.input_json,r.output_json,r.plan_json,rr.parent_id,rr.retained_until,EXISTS(SELECT 1 FROM retention_pins WHERE kind='run' AND id=r.id) FROM runs r LEFT JOIN run_retention rr ON rr.run_id=r.id`, func(rows *sql.Rows) error {
		rec := &record{}
		var updated string
		var input, output, plan, parent, until sql.NullString
		if err := rows.Scan(&rec.run.ID, &rec.run.State, &updated, &rec.run.PlanDigest, &input, &output, &plan, &parent, &until, &rec.run.Pinned); err != nil {
			return err
		}
		t, err := time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return err
		}
		rec.run.UpdatedAt = t
		if until.Valid {
			t, err := time.Parse(time.RFC3339Nano, until.String)
			if err != nil {
				return err
			}
			rec.run.RetainedUntil = &t
		}
		rec.parent = parent.String
		rec.raw = []string{input.String, output.String, plan.String}
		runs[rec.run.ID] = rec
		return nil
	})
	if err != nil {
		return nil, err
	}
	// A terminal run may still have an unfinished worker or an effect awaiting
	// reconciliation. Those scopes remain live independently of the run deadline.
	if err := gcRows(tx, `SELECT run_id FROM execution_attempts WHERE state NOT IN ('succeeded','failed','cancelled','resolved') OR await_reconciliation_reason<>'' UNION SELECT run_id FROM assignments WHERE state IN ('active','uncertain')`, func(rows *sql.Rows) error {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		if rec := runs[id]; rec != nil {
			rec.run.Pinned = true
		}
		return nil
	}); err != nil {
		return nil, err
	}
	protected := map[string]bool{}
	for id, rec := range runs {
		if !policy.ExpiredFor(&rec.run, policy.Artifacts, now) || !policy.ExpiredFor(&rec.run, policy.EventHistory, now) || !policy.ExpiredFor(&rec.run, policy.StdoutStderr, now) || (policy.Workspaces > 0 && !policy.ExpiredFor(&rec.run, policy.Workspaces, now)) {
			for next := id; next != "" && !protected[next]; {
				protected[next] = true
				ancestor, ok := runs[next]
				if !ok {
					return nil, fmt.Errorf("missing retention parent %s", next)
				}
				next = ancestor.parent
			}
		}
	}
	// Only ancestors receive the all-category protection; a run's own category
	// deadlines stay independent.
	ancestors := map[string]bool{}
	for id := range protected {
		if parent := runs[id].parent; parent != "" {
			ancestors[parent] = true
		}
	}
	reachable := map[string]bool{}
	if err := gcRows(tx, `SELECT id FROM retention_pins WHERE kind='artifact'`, func(rows *sql.Rows) error {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		reachable[id] = true
		return nil
	}); err != nil {
		return nil, err
	}
	plans := map[string]bool{}
	expiredArtifacts := map[string]bool{}
	expiredEvents := map[string]bool{}
	workspaceRoot := ""
	if len(workspaceRoots) > 0 {
		workspaceRoot = workspaceRoots[0]
	}
	if workspaceRoot != "" {
		if info, err := os.Lstat(workspaceRoot); err != nil && !os.IsNotExist(err) {
			return nil, err
		} else if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return nil, fmt.Errorf("invalid workspace root")
		}
	}
	for id, rec := range runs {
		r := rec.run
		if ancestors[id] {
			r.Pinned = true
		}
		expiredArtifacts[id] = policy.ExpiredFor(&r, policy.Artifacts, now)
		expiredEvents[id] = policy.ExpiredFor(&r, policy.EventHistory, now)
		if !expiredArtifacts[id] {
			plans[r.PlanDigest] = true
			for _, raw := range rec.raw {
				if err := markJSON(raw, reachable); err != nil {
					return nil, err
				}
			}
		}
		if policy.ExpiredFor(&r, policy.StdoutStderr, now) {
			report.Logs[id] = 0
		}
		if workspaceRoot != "" && policy.ExpiredFor(&r, policy.Workspaces, now) {
			candidate, err := runtimefiles.WorkspacePreview(workspaceRoot, id)
			if err != nil {
				return nil, err
			}
			if candidate != nil {
				report.Workspaces = append(report.Workspaces, *candidate)
			}
		}
	}
	// Scan references from persisted scopes, execution results and pending inputs.
	for _, query := range []string{
		`SELECT run_id,outputs_json FROM activations`, `SELECT run_id,inputs_json FROM activations`,
		`SELECT run_id,result_json FROM execution_attempts`, `SELECT run_id,operation_ref_json FROM execution_attempts`,
		`SELECT COALESCE(a.run_id,d.run_id,''),w.result_json FROM worker_results w LEFT JOIN execution_attempts a ON a.id=w.attempt_id LEFT JOIN dispatches d ON d.attempt_id=w.attempt_id`,
		`SELECT COALESCE(a.run_id,d.run_id,''),w.payload_json FROM worker_diagnostics w LEFT JOIN execution_attempts a ON a.id=w.attempt_id LEFT JOIN dispatches d ON d.attempt_id=w.attempt_id`,
		`SELECT run_id,request_json FROM dispatches`,
		`SELECT run_id,response_json FROM input_requests`, `SELECT run_id,schema_json FROM input_requests`, `SELECT run_id,payload_json FROM signals`,
		`SELECT run_id,payload_json FROM timers`, `SELECT run_id,queue_json FROM session_input_queues`,
	} {
		if err := gcRows(tx, query, func(rows *sql.Rows) error {
			var id string
			var raw sql.NullString
			if err := rows.Scan(&id, &raw); err != nil {
				return err
			}
			if !expiredArtifacts[id] {
				return markJSON(raw.String, reachable)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	// Subscription fanout reads the event history directly, independently of the
	// outbox. Keep unread events until every enabled dispatcher cursor passes them.
	unreadAfter := map[string]int{}
	if err := gcRows(tx, `SELECT r.id,MIN(COALESCE(c.seq,0)) FROM runs r CROSS JOIN subscriptions s LEFT JOIN subscription_cursors c ON c.id=s.id||':'||r.id WHERE json_extract(s.definition_json,'$.enabled')=1 GROUP BY r.id`, func(rows *sql.Rows) error {
		var id string
		var seq int
		if err := rows.Scan(&id, &seq); err != nil {
			return err
		}
		unreadAfter[id] = seq
		return nil
	}); err != nil {
		return nil, err
	}
	eventSeqs := map[string][]int{}
	if err := gcRows(tx, `SELECT run_id,seq,type,payload_json FROM events ORDER BY run_id,seq`, func(rows *sql.Rows) error {
		var id, kind string
		var seq int
		var raw sql.NullString
		if err := rows.Scan(&id, &seq, &kind, &raw); err != nil {
			return err
		}
		cursor, subscribed := unreadAfter[id]
		if expiredEvents[id] && (!subscribed || seq <= cursor) && (kind != state.EventArtifactSnapshot || expiredArtifacts[id]) {
			eventSeqs[id] = append(eventSeqs[id], seq)
			report.Events[id]++
			return nil
		}
		return markJSON(raw.String, reachable)
	}); err != nil {
		return nil, err
	}
	for _, query := range []string{
		`SELECT payload_json FROM outbox WHERE published_at IS NULL`,
		`SELECT record_json FROM subscription_deliveries WHERE state IN ('pending','requeued','deadletter')`,
	} {
		if err := gcRows(tx, query, func(rows *sql.Rows) error {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			return markJSON(raw, reachable)
		}); err != nil {
			return nil, err
		}
	}
	// Configured schedules/triggers and explicit pins keep their immutable plans.
	for _, query := range []string{`SELECT plan_json FROM schedules`, `SELECT definition_json FROM schedules`, `SELECT definition_json FROM triggers`, `SELECT definition_json FROM subscriptions`} {
		if err := gcRows(tx, query, func(rows *sql.Rows) error {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			if err := markJSON(raw, plans); err != nil {
				return err
			}
			return markJSON(raw, reachable)
		}); err != nil {
			return nil, err
		}
	}
	if err := gcRows(tx, `SELECT id FROM retention_pins WHERE kind='plan'`, func(rows *sql.Rows) error {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		plans[id] = true
		return nil
	}); err != nil {
		return nil, err
	}
	type version struct {
		raw     string
		created time.Time
	}
	versions := map[string]version{}
	if err := gcRows(tx, `SELECT digest,plan_json,created_at FROM workflow_versions`, func(rows *sql.Rows) error {
		var digest, raw, created string
		if err := rows.Scan(&digest, &raw, &created); err != nil {
			return err
		}
		t, err := time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return err
		}
		versions[digest] = version{raw, t}
		if policy.Caches == 0 || !now.After(t.Add(policy.Caches)) {
			plans[digest] = true
		}
		return nil
	}); err != nil {
		return nil, err
	}
	for digest := range plans {
		reachable[digest] = true
	}
	// Plans may themselves refer to other registered plans. Compute a closure
	// before deciding which cache entries are unreferenced.
	markedPlans := map[string]bool{}
	for {
		changed := false
		for digest, v := range versions {
			if markedPlans[digest] || (!plans[digest] && !reachable[digest]) {
				continue
			}
			if err := markJSON(v.raw, reachable); err != nil {
				return nil, err
			}
			markedPlans[digest] = true
			changed = true
		}
		if !changed {
			break
		}
	}
	for digest := range versions {
		if !markedPlans[digest] {
			report.Plans = append(report.Plans, digest)
		}
	}
	// Bind approval to the actual log rows, not just their count. Logs may have
	// changed between offline maintenance sessions without changing that count.
	logRows := map[string][]string{}
	if err := gcRows(tx, `SELECT run_id,activation_id,stream,seq,data,timestamp FROM log_chunks ORDER BY run_id,activation_id,stream,seq`, func(rows *sql.Rows) error {
		var id, activation, stream, timestamp string
		var seq int
		var data []byte
		if err := rows.Scan(&id, &activation, &stream, &seq, &data, &timestamp); err != nil {
			return err
		}
		if _, expired := report.Logs[id]; expired {
			raw, err := json.Marshal([]any{activation, stream, seq, data, timestamp})
			if err != nil {
				return err
			}
			sum := sha256.Sum256(raw)
			logRows[id] = append(logRows[id], hex.EncodeToString(sum[:]))
			report.Logs[id]++
		}
		return nil
	}); err != nil {
		return nil, err
	}
	for id, count := range report.Logs {
		if count == 0 {
			delete(report.Logs, id)
		}
	}
	sort.Strings(report.Plans)
	sort.Slice(report.Workspaces, func(i, j int) bool { return report.Workspaces[i].RunID < report.Workspaces[j].RunID })
	before := now.Add(-policy.Artifacts)
	if policy.Artifacts == 0 {
		before = time.Time{}
	}
	report.Artifacts, err = local.Candidates(reachable, before)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(struct {
		Report    *GCReport
		Sequences map[string][]int
		LogRows   map[string][]string
	}{report, eventSeqs, logRows})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	report.Token = hex.EncodeToString(sum[:])
	if approval == "" {
		return report, nil
	}
	if approval != report.Token {
		return report, fmt.Errorf("GC preview changed; review the new dry-run report")
	}
	for _, candidate := range report.Workspaces {
		if err := runtimefiles.RemoveWorkspace(workspaceRoot, candidate); err != nil {
			return report, err
		}
	}
	for _, candidate := range report.Artifacts {
		if err := local.Expire(candidate.Digest); err != nil {
			return report, err
		}
	}
	for id, seqs := range eventSeqs {
		// Preserve event sequence numbers after history is removed and a run retried.
		if _, err := tx.Exec(`INSERT INTO event_watermarks(run_id,seq) SELECT ?,MAX(seq) FROM events WHERE run_id=? ON CONFLICT(run_id) DO UPDATE SET seq=MAX(seq,excluded.seq)`, id, id); err != nil {
			return report, err
		}
		for _, seq := range seqs {
			if _, err := tx.Exec(`DELETE FROM events WHERE run_id=? AND seq=?`, id, seq); err != nil {
				return report, err
			}
		}
	}
	for id := range report.Logs {
		if _, err := tx.Exec(`DELETE FROM log_chunks WHERE run_id=?`, id); err != nil {
			return report, err
		}
	}
	for _, digest := range report.Plans {
		if _, err := tx.Exec(`DELETE FROM workflow_versions WHERE digest=?`, digest); err != nil {
			return report, err
		}
	}
	if err := tx.Commit(); err != nil {
		return report, err
	}
	report.Applied = true
	return report, nil
}

func gcRows(tx *sql.Tx, query string, visit func(*sql.Rows) error) error {
	rows, err := tx.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := visit(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Scan strings recursively, including embedded cas:// references. Treat JSON
// decoding failures as errors so corrupt metadata never silently loses roots.
func markJSON(raw string, reachable map[string]bool) error {
	if raw == "" {
		return nil
	}
	var value any
	if err := decodePersistedValue(raw, &value); err != nil {
		return fmt.Errorf("invalid JSON in GC roots: %w", err)
	}
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case string:
			for digest := range references(v) {
				reachable[digest] = true
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		case map[string]any:
			for key, child := range v {
				walk(key)
				walk(child)
			}
		}
	}
	walk(value)
	return nil
}
func references(raw string) map[string]bool {
	result := map[string]bool{}
	for {
		i := strings.Index(raw, "sha256:")
		if i < 0 {
			break
		}
		raw = raw[i+7:]
		if len(raw) < 64 {
			break
		}
		digest := raw[:64]
		if _, err := hex.DecodeString(digest); err == nil && strings.ToLower(digest) == digest {
			result["sha256:"+digest] = true
		}
	}
	return result
}
