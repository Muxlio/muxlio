package state

import (
	"encoding/json"
	"time"
)

// OperationalItem is a recorded coordinator fact, never an inferred outcome.
type OperationalItem struct {
	Kind         string `json:"kind"`
	RunID        string `json:"runId"`
	ActivationID string `json:"activationId,omitempty"`
	AttemptID    string `json:"attemptId,omitempty"`
	State        string `json:"state"`
	Reason       string `json:"reason,omitempty"`
	At           string `json:"at,omitempty"`
}
type RetentionPin struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type OperationalReport struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Name       string          `json:"name"`
	ImportedAt time.Time       `json:"importedAt"`
	Document   json.RawMessage `json:"document,omitempty"`
}
type OperationsStore interface {
	AutomationHistoryPage(string, string, int, int) ([]json.RawMessage, error)
	ArtifactRevisionPage(string, int, int) ([]Event, error)
	SetAutomationEnabled(string, string, bool) error
	OperationalPage(string, int, int) ([]OperationalItem, map[string]int, error)
	ListRetentionPins() ([]RetentionPin, error)
	PinRetention(string, string, bool) error
	SetRunRetention(string, string, *time.Time) error
	SaveOperationalReport(OperationalReport) error
	ListOperationalReports(int, int) ([]OperationalReport, error)
	GetOperationalReport(string) (*OperationalReport, error)
}
