package sqlite

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/muxlio/muxlio-core/internal/schedule"
	"github.com/muxlio/muxlio-core/internal/state"
)

var _ schedule.Store = (*Store)(nil)

// CreateSchedule persists a definition whose target/input were checked with
// plan.ValidateSchedule by the submission layer, as with ordinary run creation.
func (s *Store) CreateSchedule(d schedule.Definition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw []byte
	if err := tx.QueryRow(`SELECT plan_json FROM workflow_versions WHERE digest=?`, d.WorkflowDigest).Scan(&raw); err != nil {
		return fmt.Errorf("registered workflow version required: %w", err)
	}
	var p struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if err := d.Validate(); err != nil {
		return err
	}
	if p.Digest != d.WorkflowDigest {
		return fmt.Errorf("workflow pin does not match registered digest")
	}
	next, err := d.First()
	if err != nil {
		return err
	}
	definition, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO schedules(id,definition_json,plan_json,next_fire) VALUES(?,?,?,?)`, d.ID, definition, raw, scheduleTime(next)); err != nil {
		return err
	}
	return tx.Commit()
}

func scheduleTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func (s *Store) ListSchedules() ([]schedule.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT definition_json,next_fire,EXISTS(SELECT 1 FROM automation_switches x WHERE x.kind='schedules' AND x.id=schedules.id AND x.enabled=0) FROM schedules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []schedule.Schedule{}
	for rows.Next() {
		var item schedule.Schedule
		var raw []byte
		var next sql.NullString
		if err := rows.Scan(&raw, &next, &item.Paused); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &item.Definition); err != nil {
			return nil, err
		}
		if next.Valid {
			t, err := time.Parse(time.RFC3339Nano, next.String)
			if err != nil {
				return nil, err
			}
			item.NextFire = &t
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListScheduleFires(id string) ([]schedule.Fire, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT due_at,through_at,state,COALESCE(run_id,'') FROM schedule_fires WHERE schedule_id=? ORDER BY due_at`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []schedule.Fire{}
	for rows.Next() {
		f := schedule.Fire{ScheduleID: id}
		var due string
		var through sql.NullString
		if err := rows.Scan(&due, &through, &f.State, &f.RunID); err != nil {
			return nil, err
		}
		f.DueAt, err = time.Parse(time.RFC3339Nano, due)
		if err != nil {
			return nil, err
		}
		if through.Valid {
			t, err := time.Parse(time.RFC3339Nano, through.String)
			if err != nil {
				return nil, err
			}
			f.Through = &t
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// TickSchedules atomically advances cursors, records decisions, and creates
// pinned runs. No durable "claimed but not created" gap exists at a crash.
func (s *Store) TickSchedules(now time.Time) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Acquire the SQLite write lock before reading any cursor, including when
	// different Store handles tick concurrently. A lock failure is retryable.
	if _, err := tx.Exec(`UPDATE schedules SET next_fire=next_fire`); err != nil {
		return nil, err
	}
	rows, err := tx.Query(`SELECT definition_json,plan_json,next_fire FROM schedules WHERE NOT EXISTS(SELECT 1 FROM automation_switches x WHERE x.kind='schedules' AND x.id=schedules.id AND x.enabled=0) ORDER BY id`)
	if err != nil {
		return nil, err
	}
	type entry struct {
		d    schedule.Definition
		raw  []byte
		next *time.Time
	}
	entries := []entry{}
	for rows.Next() {
		var e entry
		var definition []byte
		var next sql.NullString
		if err := rows.Scan(&definition, &e.raw, &next); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal(definition, &e.d); err != nil {
			rows.Close()
			return nil, err
		}
		if next.Valid {
			t, err := time.Parse(time.RFC3339Nano, next.String)
			if err != nil {
				rows.Close()
				return nil, err
			}
			e.next = &t
		}
		entries = append(entries, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		var p struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(e.raw, &p); err != nil {
			return nil, err
		}
		next := e.next
		for count := 0; next != nil && !next.After(now) && count < e.d.MaxCatchUp; count++ {
			if _, err := tx.Exec(`INSERT INTO schedule_fires(schedule_id,due_at,state) VALUES(?,?,'queued')`, e.d.ID, scheduleTime(next)); err != nil {
				return nil, err
			}
			next, err = e.d.Next(*next)
			if err != nil {
				return nil, err
			}
		}
		if next != nil && !next.After(now) {
			if _, err := tx.Exec(`INSERT INTO schedule_fires(schedule_id,due_at,through_at,state) VALUES(?,?,?,'catch_up_limit')`, e.d.ID, scheduleTime(next), scheduleTime(&now)); err != nil {
				return nil, err
			}
			next, err = e.d.Next(now)
			if err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(`UPDATE schedules SET next_fire=? WHERE id=?`, scheduleTime(next), e.d.ID); err != nil {
			return nil, err
		}
		var active int
		if err := tx.QueryRow(`SELECT count(*) FROM schedule_fires f JOIN runs r ON r.id=f.run_id WHERE f.schedule_id=? AND r.state NOT IN ('succeeded','failed','cancelled')`, e.d.ID).Scan(&active); err != nil {
			return nil, err
		}
		pending, err := tx.Query(`SELECT due_at FROM schedule_fires WHERE schedule_id=? AND state='queued' ORDER BY due_at`, e.d.ID)
		if err != nil {
			return nil, err
		}
		dueTimes := []string{}
		for pending.Next() {
			var due string
			if err := pending.Scan(&due); err != nil {
				pending.Close()
				return nil, err
			}
			dueTimes = append(dueTimes, due)
		}
		err = pending.Err()
		pending.Close()
		if err != nil {
			return nil, err
		}
		for _, due := range dueTimes {
			if active > 0 && e.d.Overlap == "queue" {
				break
			}
			if active > 0 && e.d.Overlap == "skip" {
				if _, err := tx.Exec(`UPDATE schedule_fires SET state='overlap_skipped' WHERE schedule_id=? AND due_at=?`, e.d.ID, due); err != nil {
					return nil, err
				}
				continue
			}
			hash := sha256.Sum256([]byte(e.d.ID + "\x00" + due))
			id := "scheduled_" + hex.EncodeToString(hash[:])
			_, err := tx.Exec(`INSERT INTO runs(id,workflow_name,state,input_json,error,created_at,updated_at,plan_json,plan_digest) VALUES(?,?,'created',?,'',?,?,?,?)`, id, p.Name, string(e.d.Input), rfc3339(now), rfc3339(now), e.raw, e.d.WorkflowDigest)
			if err != nil {
				return nil, err
			}
			if err := appendEvent(tx, id, state.EventRunCreated, id, map[string]any{"scheduleId": e.d.ID, "dueAt": due}); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(`UPDATE schedule_fires SET state='started',run_id=? WHERE schedule_id=? AND due_at=?`, id, e.d.ID, due); err != nil {
				return nil, err
			}
			active++
		}
	}
	// Include previously committed but not yet launched runs, so an interrupted
	// launch in this process is retried as well as recovered on restart.
	ready, err := tx.Query(`SELECT r.id FROM schedule_fires f JOIN runs r ON r.id=f.run_id WHERE r.state='created' ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for ready.Next() {
		var id string
		if err := ready.Scan(&id); err != nil {
			ready.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = ready.Err()
	ready.Close()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}
