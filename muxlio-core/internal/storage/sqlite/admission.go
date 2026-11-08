package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

// ConfigureAdmission persists budgets shared by every polling worker. Existing
// reservations retain their slot cost until their assignment becomes terminal.
func (s *Store) ConfigureAdmission(cfg state.AdmissionConfig) error {
	if cfg.GlobalCap < 0 || cfg.PerRunCap < 0 {
		return fmt.Errorf("admission caps must be non-negative")
	}
	for _, caps := range []map[string]int{cfg.ExecutorCaps, cfg.KeyCaps, cfg.ProviderCaps} {
		for key, cap := range caps {
			if key == "" || cap < 0 {
				return fmt.Errorf("invalid admission cap for %q", key)
			}
		}
	}
	for key, rate := range cfg.ProviderRates {
		if key == "" || rate.Burst < 1 || rate.PerSecond <= 0 || math.IsNaN(rate.PerSecond) || math.IsInf(rate.PerSecond, 0) {
			return fmt.Errorf("invalid admission rate for %q", key)
		}
	}
	for key, demand := range cfg.Executors {
		if key == "" || demand.Slots < 0 {
			return fmt.Errorf("invalid admission demand for %q", key)
		}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO admission_config(id,config_json) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET config_json=excluded.config_json`, string(raw))
	if err != nil {
		return err
	}
	// Upgrade active legacy assignments before budgets take effect. Existing
	// reservations keep the resource keys and costs used at admission time.
	rows, err := tx.Query(`SELECT a.activation_id,d.executor FROM assignments a JOIN dispatches d ON d.activation_id=a.activation_id LEFT JOIN admission_reservations r ON r.activation_id=a.activation_id WHERE a.state='active' AND r.activation_id IS NULL`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, executor string
		if err := rows.Scan(&id, &executor); err != nil {
			rows.Close()
			return err
		}
		if err := reserveSlots(tx, id, admissionDemand(cfg, executor)); err != nil {
			rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	return tx.Commit()
}

func admissionDemand(cfg state.AdmissionConfig, executor string) state.AdmissionDemand {
	demand := cfg.Executors[executor]
	if demand.Slots == 0 {
		demand.Slots = 1
	}
	if demand.ExecutorKey == "" {
		demand.ExecutorKey = executor
	}
	return demand
}

func readAdmissionConfig(tx *sql.Tx) (state.AdmissionConfig, error) {
	var cfg state.AdmissionConfig
	var raw string
	err := tx.QueryRow(`SELECT config_json FROM admission_config WHERE id=1`).Scan(&raw)
	if err == sql.ErrNoRows {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal([]byte(raw), &cfg)
	return cfg, err
}

// Only active assignments consume execution capacity. Timers and human input
// are interpreter waits, have no dispatch, and never create a reservation.
func admissionReason(tx *sql.Tx, cfg state.AdmissionConfig, d state.Dispatch, demand state.AdmissionDemand, workerID string, workerCap int, now time.Time) (string, error) {
	var request workers.ExecuteRequest
	if err := decodePersistedValue(string(d.Request), &request); err != nil {
		return "", err
	}
	if err := workers.ValidateWorkspaceContract(request.WorkspaceMode, request.ConcurrencyKeys); err != nil {
		return "", err
	}
	for _, key := range workers.ExclusiveDispatchKeys(request) {
		var held int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM exclusive_resources WHERE resource_key=?`, key).Scan(&held); err != nil {
			return "", err
		}
		if held != 0 {
			return "Waiting for exclusive resource key", nil
		}
	}
	scopes := []struct {
		cap       int
		predicate string
		key       string
		reason    string
	}{
		{workerCap, "a.worker_id=?", workerID, "Waiting for worker capacity"},
		{cfg.GlobalCap, "1=1", "", "Waiting for global capacity"},
		{cfg.PerRunCap, "a.run_id=?", d.RunID, "Waiting for run capacity"},
		{cfg.ExecutorCaps[demand.ExecutorKey], "COALESCE(r.executor,d.executor)=?", demand.ExecutorKey, "Waiting for executor capacity"},
		{cfg.KeyCaps[demand.OperatorKey], "r.operator_key=?", demand.OperatorKey, "Waiting for operator key capacity"},
		{cfg.ProviderCaps[demand.ProviderKey], "r.provider_key=?", demand.ProviderKey, "Waiting for provider capacity"},
	}
	for i, scope := range scopes {
		if scope.cap == 0 && i != 0 {
			continue
		}
		var used int
		args := []any{scope.key}
		if scope.predicate == "1=1" {
			args = nil
		}
		err := tx.QueryRow(`SELECT COALESCE(SUM(COALESCE(r.slots,1)),0) FROM assignments a LEFT JOIN admission_reservations r ON r.activation_id=a.activation_id LEFT JOIN dispatches d ON d.activation_id=a.activation_id WHERE a.state='active' AND (`+scope.predicate+`)`, args...).Scan(&used)
		if err != nil {
			return "", err
		}
		if demand.Slots > scope.cap-used {
			return scope.reason, nil
		}
	}
	if rate, ok := cfg.ProviderRates[demand.ProviderKey]; ok {
		tokens, err := admissionTokens(tx, demand.ProviderKey, rate, now)
		if err != nil {
			return "", err
		}
		if tokens < 1 {
			return "Waiting for provider rate limit", nil
		}
	}
	return "", nil
}

func admissionTokens(tx *sql.Tx, key string, rate state.AdmissionRate, now time.Time) (float64, error) {
	var tokens float64
	var updated string
	err := tx.QueryRow(`SELECT tokens,updated_at FROM admission_buckets WHERE provider_key=?`, key).Scan(&tokens, &updated)
	if err == sql.ErrNoRows {
		return float64(rate.Burst), nil
	}
	if err != nil {
		return 0, err
	}
	last, err := time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return 0, err
	}
	return math.Min(float64(rate.Burst), tokens+math.Max(0, now.Sub(last).Seconds())*rate.PerSecond), nil
}

func reserveAdmission(tx *sql.Tx, cfg state.AdmissionConfig, d state.Dispatch, demand state.AdmissionDemand, now time.Time) error {
	var request workers.ExecuteRequest
	if err := decodePersistedValue(string(d.Request), &request); err != nil {
		return err
	}
	// Reservations outlive revocation and lease expiry. Only confirmed
	// completion or explicit reconciliation proves the old process stopped.
	seen := make(map[string]bool)
	for _, key := range workers.ExclusiveDispatchKeys(request) {
		if seen[key] {
			continue
		}
		seen[key] = true
		if _, err := tx.Exec(`INSERT INTO exclusive_resources(resource_key,attempt_id,activation_id) VALUES(?,?,?)`, key, d.AttemptID, d.ActivationID); err != nil {
			return err
		}
	}
	if err := reserveSlots(tx, d.ActivationID, demand); err != nil {
		return err
	}
	if rate, ok := cfg.ProviderRates[demand.ProviderKey]; ok {
		tokens, err := admissionTokens(tx, demand.ProviderKey, rate, now)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO admission_buckets(provider_key,tokens,updated_at) VALUES(?,?,?) ON CONFLICT(provider_key) DO UPDATE SET tokens=excluded.tokens,updated_at=excluded.updated_at`, demand.ProviderKey, tokens-1, rfc3339(now)); err != nil {
			return err
		}
	}
	// A monotonically increasing service turn implements round robin between
	// ready run keys. New runs start at zero, ahead of previously served runs;
	// blocked runs do not spend a turn or prevent eligible peers from running.
	_, err := tx.Exec(`INSERT INTO admission_turns(run_id,turn) VALUES(?,(SELECT COALESCE(MAX(turn),0)+1 FROM admission_turns)) ON CONFLICT(run_id) DO UPDATE SET turn=excluded.turn`, d.RunID)
	return err
}

func reserveSlots(tx *sql.Tx, activationID string, demand state.AdmissionDemand) error {
	if _, err := tx.Exec(`INSERT INTO admission_reservations(activation_id,slots,executor,operator_key,provider_key) VALUES(?,?,?,?,?) ON CONFLICT(activation_id) DO UPDATE SET slots=excluded.slots,executor=excluded.executor,operator_key=excluded.operator_key,provider_key=excluded.provider_key`, activationID, demand.Slots, demand.ExecutorKey, demand.OperatorKey, demand.ProviderKey); err != nil {
		return err
	}
	return nil
}
