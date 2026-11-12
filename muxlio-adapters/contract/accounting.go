package contract

import (
	"encoding/json"
	"fmt"
	"maps"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Count and Amount are decimal strings on the wire, preserving exact values in
// JavaScript and across storage/API round trips.
type Count string
type Amount string

type Reading[T any] struct {
	Value      T                 `json:"value"`
	Source     string            `json:"source"`
	Fidelity   string            `json:"fidelity"`
	Method     string            `json:"method,omitempty"`
	Version    string            `json:"version,omitempty"`
	CatalogID  string            `json:"catalogId,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

type Measure[T any] struct {
	Collected *Reading[T] `json:"collected,omitempty"`
	Estimated *Reading[T] `json:"estimated,omitempty"`
}

type TokenMeasures struct {
	Total      Measure[Count] `json:"total,omitempty"`
	Input      Measure[Count] `json:"input,omitempty"`
	Output     Measure[Count] `json:"output,omitempty"`
	CacheRead  Measure[Count] `json:"cacheRead,omitempty"`
	CacheWrite Measure[Count] `json:"cacheWrite,omitempty"`
	Reasoning  Measure[Count] `json:"reasoning,omitempty"`
}

type CreditMeasure struct {
	Provider string          `json:"provider"`
	Unit     string          `json:"unit"`
	Amount   Measure[Amount] `json:"amount"`
}

type CostMeasure struct {
	Currency string          `json:"currency"`
	Basis    string          `json:"basis"`
	Amount   Measure[Amount] `json:"amount"`
}

type AccountingEntry struct {
	ID       string          `json:"id"`
	Provider string          `json:"provider,omitempty"`
	Model    string          `json:"model,omitempty"`
	Tokens   *TokenMeasures  `json:"tokens,omitempty"`
	Credits  []CreditMeasure `json:"credits,omitempty"`
	Costs    []CostMeasure   `json:"costs,omitempty"`
}

type AccountingKey struct {
	Kind     string `json:"kind"`
	Provider string `json:"provider,omitempty"`
	Unit     string `json:"unit,omitempty"`
	Currency string `json:"currency,omitempty"`
	Basis    string `json:"basis,omitempty"`
}

type MeterCoverage struct {
	Key    AccountingKey `json:"key"`
	State  string        `json:"state"`
	Reason string        `json:"reason,omitempty"`
}

type Accounting struct {
	Revision uint64              `json:"revision"`
	Final    bool                `json:"final"`
	Entries  []AccountingEntry   `json:"entries,omitempty"`
	Coverage []MeterCoverage     `json:"coverage,omitempty"`
	Models   []AccountingModel   `json:"models,omitempty"`
	Sessions []AccountingSession `json:"sessions,omitempty"`
}

// AccountingSession identifies a provider-owned execution without persisting
// transcripts or provider credentials. It does not by itself prove cost scope.
type AccountingSession struct {
	ID         string `json:"id"`
	Provider   string `json:"provider"`
	Source     string `json:"source"`
	Version    string `json:"version,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	Principal  string `json:"principal,omitempty"`
	Isolated   bool   `json:"isolated,omitempty"`
}

// AccountingModel distinguishes configuration from evidence of a model that
// actually served work. Requested/selected identities never establish pricing.
type AccountingModel struct {
	ID     string `json:"id"`
	Role   string `json:"role"`
	Source string `json:"source"`
}

// RemoteAccountingUpdate carries a versioned full snapshot for the persisted
// owner of a remote task. The polling invocation's accounting stays separate.
type RemoteAccountingUpdate struct {
	TaskID         string     `json:"taskId"`
	SourceRevision string     `json:"sourceRevision"`
	Accounting     Accounting `json:"accounting"`
}

func (u *RemoteAccountingUpdate) Validate() error {
	if u == nil {
		return nil
	}
	if u.TaskID == "" || len(u.TaskID) > 512 || strings.ContainsAny(u.TaskID, "\r\n\x00") {
		return fmt.Errorf("invalid remote accounting task identity")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`).MatchString(u.SourceRevision) {
		return fmt.Errorf("invalid remote accounting source revision")
	}
	_, err := CanonicalAccounting(u.Accounting)
	return err
}

var accountingID = regexp.MustCompile(`^[a-z0-9._:-]{1,120}$`)
var accountingMeta = regexp.MustCompile(`^[a-z0-9._:-]{1,80}$`)
var accountingMethod = regexp.MustCompile(`^[a-z0-9._:-]{1,120}$`)
var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
var amountPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]{1,18})?$`)

func (c Count) Validate() error {
	s := string(c)
	if s == "" || (len(s) > 1 && s[0] == '0') || strings.Trim(s, "0123456789") != "" {
		return fmt.Errorf("count must be a canonical non-negative integer")
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok || n.Sign() < 0 || n.Cmp(big.NewInt(9223372036854775807)) > 0 {
		return fmt.Errorf("count is out of range")
	}
	return nil
}

func (a Amount) Validate() error {
	s := string(a)
	if !amountPattern.MatchString(s) {
		return fmt.Errorf("amount must be a canonical non-negative decimal with at most 18 fractional digits")
	}
	if strings.HasSuffix(s, ".") || (strings.Contains(s, ".") && strings.HasSuffix(s, "0")) {
		return fmt.Errorf("amount must not contain redundant fractional zeros")
	}
	digits := strings.ReplaceAll(s, ".", "")
	if len(strings.TrimLeft(digits, "0")) > 38 {
		return fmt.Errorf("amount exceeds 38 significant digits")
	}
	return nil
}

func validateReading[T any](r *Reading[T], validate func(T) error, collected bool) error {
	if r == nil {
		return nil
	}
	if !accountingMeta.MatchString(r.Source) {
		return fmt.Errorf("invalid accounting source")
	}
	switch r.Fidelity {
	case "metered", "derived-exact", "provider-estimate", "proxy":
	default:
		return fmt.Errorf("invalid accounting fidelity")
	}
	if !collected && (r.Method == "" || r.Version == "") {
		return fmt.Errorf("estimated readings require method and version")
	}
	if r.Method != "" && !accountingMethod.MatchString(r.Method) {
		return fmt.Errorf("invalid accounting method")
	}
	if r.Version != "" && (len(r.Version) > 80 || !accountingMeta.MatchString(r.Version)) {
		return fmt.Errorf("invalid accounting version")
	}
	if r.CatalogID != "" && (len(r.CatalogID) > 120 || !accountingMeta.MatchString(strings.ToLower(r.CatalogID))) {
		return fmt.Errorf("invalid accounting catalog id")
	}
	if len(r.Attributes) > 16 {
		return fmt.Errorf("too many accounting attributes")
	}
	for k, v := range r.Attributes {
		if !accountingMethod.MatchString(k) || len(v) > 256 || strings.ContainsAny(v, "\r\n\x00") {
			return fmt.Errorf("invalid accounting attribute")
		}
	}
	return validate(r.Value)
}

func validateMeasure[T any](m Measure[T], validate func(T) error) error {
	if err := validateReading(m.Collected, validate, true); err != nil {
		return err
	}
	return validateReading(m.Estimated, validate, false)
}

func (a *Accounting) Validate() error {
	if a == nil {
		return nil
	}
	if a.Revision == 0 {
		return fmt.Errorf("accounting revision must be positive")
	}
	if a.Revision > 9007199254740991 {
		return fmt.Errorf("accounting revision exceeds exact JSON integer range")
	}
	if len(a.Entries) > 256 || len(a.Coverage) > 64 || len(a.Models) > 32 || len(a.Sessions) > 32 {
		return fmt.Errorf("accounting snapshot exceeds limits")
	}
	sessions := map[AccountingSession]bool{}
	for _, session := range a.Sessions {
		if session.ID == "" || len(session.ID) > 512 || strings.ContainsAny(session.ID, "\r\n\x00") || !accountingMeta.MatchString(session.Provider) || !accountingMeta.MatchString(session.Source) || session.Version != "" && !accountingMeta.MatchString(session.Version) || sessions[session] {
			return fmt.Errorf("invalid or duplicate accounting session")
		}
		if len(session.Principal) > 256 || strings.ContainsAny(session.Principal, "\r\n\x00") {
			return fmt.Errorf("invalid accounting session principal")
		}
		if session.StartedAt != "" {
			start, err := time.Parse(time.RFC3339Nano, session.StartedAt)
			if err != nil {
				return fmt.Errorf("invalid accounting session start")
			}
			if session.FinishedAt != "" {
				end, err := time.Parse(time.RFC3339Nano, session.FinishedAt)
				if err != nil || end.Before(start) {
					return fmt.Errorf("invalid accounting session interval")
				}
			}
		} else if session.FinishedAt != "" {
			return fmt.Errorf("accounting session end without start")
		}
		sessions[session] = true
	}
	models := map[AccountingModel]bool{}
	for _, m := range a.Models {
		if m.ID == "" || len(m.ID) > 160 || strings.ContainsAny(m.ID, "\r\n\x00") || !accountingMeta.MatchString(m.Source) || (m.Role != "requested" && m.Role != "selected" && m.Role != "served") || models[m] {
			return fmt.Errorf("invalid or duplicate accounting model report")
		}
		models[m] = true
	}
	ids := map[string]bool{}
	for _, e := range a.Entries {
		if !accountingID.MatchString(e.ID) || ids[e.ID] {
			return fmt.Errorf("invalid or duplicate accounting entry id")
		}
		ids[e.ID] = true
		if e.Provider != "" && !accountingMeta.MatchString(e.Provider) {
			return fmt.Errorf("invalid accounting provider")
		}
		if e.Model != "" && (len(e.Model) > 160 || strings.ContainsAny(e.Model, "\r\n")) {
			return fmt.Errorf("invalid accounting model")
		}
		if e.Tokens != nil {
			for name, m := range map[string]Measure[Count]{"total": e.Tokens.Total, "input": e.Tokens.Input, "output": e.Tokens.Output, "cacheRead": e.Tokens.CacheRead, "cacheWrite": e.Tokens.CacheWrite, "reasoning": e.Tokens.Reasoning} {
				if err := validateMeasure(m, func(v Count) error { return v.Validate() }); err != nil {
					return fmt.Errorf("tokens.%s: %w", name, err)
				}
			}
		}
		credits := map[string]bool{}
		for _, c := range e.Credits {
			if !accountingMeta.MatchString(c.Provider) || !accountingMeta.MatchString(c.Unit) {
				return fmt.Errorf("invalid credit identity")
			}
			k := c.Provider + "\x00" + c.Unit
			if credits[k] {
				return fmt.Errorf("duplicate credit key")
			}
			credits[k] = true
			if err := validateMeasure(c.Amount, func(v Amount) error { return v.Validate() }); err != nil {
				return fmt.Errorf("credit: %w", err)
			}
		}
		costs := map[string]bool{}
		for _, c := range e.Costs {
			if !currencyCode.MatchString(c.Currency) || (c.Basis != "api-equivalent" && c.Basis != "plan-debit" && c.Basis != "unspecified" && c.Basis != "credit-replacement" && c.Basis != "provider-metered" && c.Basis != "model-reference") {
				return fmt.Errorf("invalid cost identity")
			}
			k := c.Currency + "\x00" + c.Basis
			if costs[k] {
				return fmt.Errorf("duplicate cost key")
			}
			costs[k] = true
			if err := validateMeasure(c.Amount, func(v Amount) error { return v.Validate() }); err != nil {
				return fmt.Errorf("cost: %w", err)
			}
		}
	}
	coverage := map[string]bool{}
	for _, c := range a.Coverage {
		k := c.Key
		if k.Kind != "tokens" && k.Kind != "credits" && k.Kind != "cost" {
			return fmt.Errorf("invalid accounting coverage kind")
		}
		if c.State != "complete" && c.State != "partial" && c.State != "unknown" && c.State != "inapplicable" {
			return fmt.Errorf("invalid accounting coverage state")
		}
		if c.Reason != "" && !accountingMeta.MatchString(c.Reason) {
			return fmt.Errorf("invalid accounting coverage reason")
		}
		ident := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s", k.Kind, k.Provider, k.Unit, k.Currency, k.Basis)
		if coverage[ident] {
			return fmt.Errorf("duplicate accounting coverage key")
		}
		coverage[ident] = true
		if k.Kind == "credits" && ((k.Provider == "") != (k.Unit == "")) {
			return fmt.Errorf("credit coverage identity must include both provider and unit")
		}
		if k.Kind == "credits" && k.Provider == "" && (c.State != "unknown" && c.State != "inapplicable") {
			return fmt.Errorf("identity-free credit coverage must be unknown or inapplicable")
		}
		if k.Kind == "cost" && ((k.Currency == "") != (k.Basis == "")) {
			return fmt.Errorf("cost coverage identity must include both currency and basis")
		}
		if k.Kind == "cost" && k.Currency == "" && (c.State != "unknown" && c.State != "inapplicable") {
			return fmt.Errorf("identity-free cost coverage must be unknown or inapplicable")
		}
		if k.Kind == "credits" && (k.Currency != "" || k.Basis != "") || k.Kind == "cost" && k.Unit != "" || k.Kind == "tokens" && (k.Provider != "" || k.Unit != "" || k.Currency != "" || k.Basis != "") {
			return fmt.Errorf("accounting coverage key has fields outside its meter kind")
		}
		if k.Kind == "cost" {
			if k.Currency != "" && !currencyCode.MatchString(k.Currency) {
				return fmt.Errorf("invalid accounting coverage currency")
			}
			if k.Basis != "" && k.Basis != "api-equivalent" && k.Basis != "plan-debit" && k.Basis != "unspecified" && k.Basis != "credit-replacement" && k.Basis != "provider-metered" && k.Basis != "model-reference" {
				return fmt.Errorf("invalid accounting coverage basis")
			}
		}
		if k.Kind == "credits" {
			if k.Provider != "" && !accountingMeta.MatchString(k.Provider) {
				return fmt.Errorf("invalid accounting coverage provider")
			}
			if k.Unit != "" && !accountingMeta.MatchString(k.Unit) {
				return fmt.Errorf("invalid accounting coverage unit")
			}
		}
	}
	for _, e := range a.Entries {
		if e.Tokens == nil {
			continue
		}
		for _, readings := range [][5]*Reading[Count]{
			{e.Tokens.CacheRead.Collected, e.Tokens.CacheWrite.Collected, e.Tokens.Input.Collected, e.Tokens.Reasoning.Collected, e.Tokens.Output.Collected},
			{e.Tokens.CacheRead.Estimated, e.Tokens.CacheWrite.Estimated, e.Tokens.Input.Estimated, e.Tokens.Reasoning.Estimated, e.Tokens.Output.Estimated},
		} {
			cr, cw, in, reasoning, output := readings[0], readings[1], readings[2], readings[3], readings[4]
			for _, pair := range [][2]*Reading[Count]{{cr, in}, {cw, in}, {reasoning, output}} {
				if comparableCountReadings(pair[0], pair[1]) {
					x, _ := new(big.Int).SetString(string(pair[0].Value), 10)
					limit, _ := new(big.Int).SetString(string(pair[1].Value), 10)
					if x.Cmp(limit) > 0 {
						return fmt.Errorf("token subset exceeds inclusive count")
					}
				}
			}
			if comparableCountReadings(cr, cw, in) {
				r, _ := new(big.Int).SetString(string(cr.Value), 10)
				w, _ := new(big.Int).SetString(string(cw.Value), 10)
				i, _ := new(big.Int).SetString(string(in.Value), 10)
				if new(big.Int).Add(r, w).Cmp(i) > 0 {
					return fmt.Errorf("cache token dimensions exceed inclusive input")
				}
			}
		}
	}
	return nil
}

func comparableCountReadings(readings ...*Reading[Count]) bool {
	if len(readings) == 0 || readings[0] == nil {
		return false
	}
	first := readings[0]
	for _, r := range readings[1:] {
		if r == nil || r.Source != first.Source || r.Fidelity != first.Fidelity || r.Version != first.Version || r.Method != first.Method || !maps.Equal(r.Attributes, first.Attributes) {
			return false
		}
	}
	return true
}

// CanonicalAccounting validates and serializes an accounting snapshot with
// stable ordering, suitable for checksums and exact-revision replay checks.
func CanonicalAccounting(a Accounting) ([]byte, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	copyA := a
	copyA.Sessions = append([]AccountingSession(nil), a.Sessions...)
	sort.Slice(copyA.Sessions, func(i, j int) bool {
		x, y := copyA.Sessions[i], copyA.Sessions[j]
		if x.Provider != y.Provider {
			return x.Provider < y.Provider
		}
		if x.Source != y.Source {
			return x.Source < y.Source
		}
		if x.ID != y.ID {
			return x.ID < y.ID
		}
		return x.Version < y.Version
	})
	copyA.Models = append([]AccountingModel(nil), a.Models...)
	sort.Slice(copyA.Models, func(i, j int) bool {
		a, b := copyA.Models[i], copyA.Models[j]
		if a.Role != b.Role {
			return a.Role < b.Role
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.ID < b.ID
	})
	copyA.Entries = append([]AccountingEntry(nil), a.Entries...)
	for i := range copyA.Entries {
		copyA.Entries[i].Credits = append([]CreditMeasure(nil), copyA.Entries[i].Credits...)
		copyA.Entries[i].Costs = append([]CostMeasure(nil), copyA.Entries[i].Costs...)
		sort.Slice(copyA.Entries[i].Credits, func(x, y int) bool {
			a, b := copyA.Entries[i].Credits[x], copyA.Entries[i].Credits[y]
			if a.Provider == b.Provider {
				return a.Unit < b.Unit
			}
			return a.Provider < b.Provider
		})
		sort.Slice(copyA.Entries[i].Costs, func(x, y int) bool {
			a, b := copyA.Entries[i].Costs[x], copyA.Entries[i].Costs[y]
			if a.Currency == b.Currency {
				return a.Basis < b.Basis
			}
			return a.Currency < b.Currency
		})
	}
	sort.Slice(copyA.Entries, func(i, j int) bool { return copyA.Entries[i].ID < copyA.Entries[j].ID })
	copyA.Coverage = append([]MeterCoverage(nil), a.Coverage...)
	sort.Slice(copyA.Coverage, func(i, j int) bool {
		a, b := copyA.Coverage[i].Key, copyA.Coverage[j].Key
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if a.Unit != b.Unit {
			return a.Unit < b.Unit
		}
		if a.Currency != b.Currency {
			return a.Currency < b.Currency
		}
		return a.Basis < b.Basis
	})
	raw, err := json.Marshal(copyA)
	if err != nil {
		return nil, err
	}
	if len(raw) > 64<<10 {
		return nil, fmt.Errorf("accounting snapshot exceeds 64 KiB")
	}
	return raw, nil
}
