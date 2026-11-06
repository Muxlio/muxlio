// Package schedule defines clock-based workflow starts independently of executors.
package schedule

import (
	"encoding/json"
	"fmt"
	"time"
	_ "time/tzdata"
)

// Definition is immutable after creation. StartAt is inclusive, in absolute time.
type Definition struct {
	ID              string          `json:"id"`
	WorkflowDigest  string          `json:"workflowDigest"`
	Input           json.RawMessage `json:"input"`
	Kind            string          `json:"kind"`
	StartAt         time.Time       `json:"startAt"`
	IntervalSeconds int64           `json:"intervalSeconds,omitempty"`
	Cron            string          `json:"cron,omitempty"`
	Timezone        string          `json:"timezone,omitempty"`
	Overlap         string          `json:"overlap"`
	MaxCatchUp      int             `json:"maxCatchUp"`
}

type Schedule struct {
	Definition
	NextFire *time.Time `json:"nextFire"`
}

// Fire records a decision. catch_up_limit covers [DueAt, Through], inclusively.
type Fire struct {
	ScheduleID string     `json:"scheduleId"`
	DueAt      time.Time  `json:"dueAt"`
	Through    *time.Time `json:"through,omitempty"`
	State      string     `json:"state"`
	RunID      string     `json:"runId,omitempty"`
}

type Store interface {
	CreateSchedule(Definition) error
	ListSchedules() ([]Schedule, error)
	ListScheduleFires(string) ([]Fire, error)
	TickSchedules(time.Time) ([]string, error)
}

func (d Definition) Validate() error {
	if d.ID == "" || len(d.ID) > 128 {
		return fmt.Errorf("id must contain 1..128 characters")
	}
	for _, c := range d.ID {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return fmt.Errorf("id must use letters, digits, - or _")
		}
	}
	if d.WorkflowDigest == "" {
		return fmt.Errorf("target must be a registered immutable workflow digest")
	}
	if !json.Valid(d.Input) {
		return fmt.Errorf("input must be a JSON value")
	}
	if d.StartAt.IsZero() || d.StartAt.Year() < 1970 || d.StartAt.Year() > 9990 {
		return fmt.Errorf("startAt must be between 1970 and 9990")
	}
	if d.Overlap != "allow" && d.Overlap != "skip" && d.Overlap != "queue" {
		return fmt.Errorf("overlap must be allow, skip or queue")
	}
	if d.MaxCatchUp < 1 || d.MaxCatchUp > 1000 {
		return fmt.Errorf("maxCatchUp must be between 1 and 1000")
	}
	switch d.Kind {
	case "once":
		if d.IntervalSeconds != 0 || d.Cron != "" || d.Timezone != "" {
			return fmt.Errorf("once only accepts startAt")
		}
	case "interval":
		if d.IntervalSeconds < 1 || d.IntervalSeconds > 315360000 || d.Cron != "" || d.Timezone != "" {
			return fmt.Errorf("interval requires intervalSeconds between 1 and 315360000")
		}
	case "cron":
		if d.IntervalSeconds != 0 || d.Timezone == "" || d.Timezone == "Local" {
			return fmt.Errorf("cron requires an explicit IANA timezone")
		}
		if _, err := time.LoadLocation(d.Timezone); err != nil {
			return err
		}
		if _, err := parseCron(d.Cron); err != nil {
			return err
		}
	default:
		return fmt.Errorf("kind must be once, interval or cron")
	}
	_, err := d.First()
	return err
}

func (d Definition) First() (*time.Time, error) {
	t := d.StartAt.UTC()
	if d.Kind == "cron" {
		return d.Next(t.Add(-time.Nanosecond))
	}
	return &t, nil
}

// Next returns the first occurrence strictly after t. Cron walks UTC minutes:
// nonexistent local times never match; both instances of a repeated time match.
func (d Definition) Next(t time.Time) (*time.Time, error) {
	switch d.Kind {
	case "once":
		return nil, nil
	case "interval":
		step := time.Duration(d.IntervalSeconds) * time.Second
		// Compute relative to the original anchor, without duration overflow for old schedules.
		seconds := t.Unix() - d.StartAt.Unix()
		n := seconds / d.IntervalSeconds
		candidate := time.Unix(d.StartAt.Unix()+n*d.IntervalSeconds, int64(d.StartAt.Nanosecond())).UTC()
		if !candidate.After(t) {
			candidate = candidate.Add(step)
		}
		return &candidate, nil
	case "cron":
		c, err := parseCron(d.Cron)
		if err != nil {
			return nil, err
		}
		loc, err := time.LoadLocation(d.Timezone)
		if err != nil {
			return nil, err
		}
		candidate := t.UTC().Truncate(time.Minute).Add(time.Minute)
		limit := candidate.AddDate(8, 0, 0)
		for candidate.Before(limit) {
			local := candidate.In(loc)
			if c.matches(local) {
				return &candidate, nil
			}
			candidate = candidate.Add(time.Minute)
		}
		return nil, fmt.Errorf("cron has no occurrence within eight years")
	}
	return nil, fmt.Errorf("invalid schedule kind")
}
