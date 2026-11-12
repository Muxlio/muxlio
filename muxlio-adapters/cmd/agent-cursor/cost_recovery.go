package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/muxlio/muxlio-adapters/accounting"
	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
)

const cursorUsageEndpoint = "https://api.cursor.com/teams/filtered-usage-events"
const cursorLedgerScope = "provider-usage-window"

// VerifiedJoinVersion is an operator's assertion of a controlled CLI sessionId
// to Admin API conversationId experiment, not a model-pricing assumption.
// No account credential is accepted through params or canonical snapshots.
type cursorBillingConfig struct {
	VerifiedJoinVersion string `json:"verifiedJoinVersion"`
	Email               string `json:"email,omitempty"`
	ServiceAccountID    string `json:"serviceAccountId,omitempty"`
}

func (c cursorBillingConfig) principal() string {
	if c.ServiceAccountID != "" {
		return "service-account:" + c.ServiceAccountID
	}
	if c.Email != "" {
		return "user:" + c.Email
	}
	return ""
}
func cursorVersion(cli string) string {
	check := adapter.CommandCheck(contract.ProbeVersion, cli, "--version")
	if !check.Passed {
		return ""
	}
	return check.Version
}

// cursorConversation accepts only the documented stream init identity. A
// result id, account id, current model, or a timestamp is never a join key.
func cursorConversation(event map[string]json.RawMessage) string {
	var kind, subtype, id string
	_ = json.Unmarshal(event["type"], &kind)
	_ = json.Unmarshal(event["subtype"], &subtype)
	if kind != "system" || subtype != "init" {
		return ""
	}
	_ = json.Unmarshal(event["session_id"], &id)
	if id == "" || len(id) > 512 || strings.ContainsAny(id, "\r\n\x00") {
		return ""
	}
	return id
}

type cursorUsageEvent struct {
	Timestamp        string          `json:"timestamp"`
	ConversationID   string          `json:"conversationId"`
	UserEmail        string          `json:"userEmail"`
	ServiceAccountID string          `json:"serviceAccountId"`
	Model            string          `json:"model"`
	Kind             string          `json:"kind"`
	IsHeadless       *bool           `json:"isHeadless"`
	IsChargeable     *bool           `json:"isChargeable"`
	IsTokenBasedCall *bool           `json:"isTokenBasedCall"`
	ChargedCents     json.RawMessage `json:"chargedCents"`
	CursorTokenFee   json.RawMessage `json:"cursorTokenFee"`
	TokenUsage       *struct {
		TotalCents json.RawMessage `json:"totalCents"`
		Discount   json.RawMessage `json:"discountPercentOff"`
	} `json:"tokenUsage"`
}
type cursorUsagePage struct {
	Total      int `json:"totalUsageEventsCount"`
	Pagination struct {
		CurrentPage int  `json:"currentPage"`
		NumPages    int  `json:"numPages"`
		PageSize    int  `json:"pageSize"`
		HasNextPage bool `json:"hasNextPage"`
	} `json:"pagination"`
	Events []cursorUsageEvent `json:"usageEvents"`
}

func cursorCentsUSD(raw json.RawMessage) (*contract.Amount, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	cents, err := accounting.ParseJSONAmount(raw)
	if err != nil {
		return nil, err
	}
	// Dividing cents by 100 is exact. Reject excess precision rather than
	// rounding a collected provider observation into a different amount.
	// Construct the decimal by shifting the source point two places.
	parts := strings.SplitN(string(cents), ".", 2)
	digits := parts[0]
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
	}
	if len(digits) < 3 {
		digits = strings.Repeat("0", 3-len(digits)) + digits
	}
	amount, err := accounting.ParseAmount(digits[:len(digits)-2] + "." + digits[len(digits)-2:] + frac)
	if err != nil {
		return nil, err
	}
	return &amount, nil
}

// fetchCursorWindow reads a complete bounded dataset. The provider currently
// has no immutable event id; dataset refreshes replace, never append. Neither
// process completion nor pagination closure proves provider billing settlement.
func fetchCursorWindow(ctx context.Context, client *http.Client, endpoint, key string, cfg cursorBillingConfig, session contract.AccountingSession) ([]accounting.Part, error) {
	if key == "" {
		return nil, fmt.Errorf("admin-api-credential-unavailable")
	}
	if cfg.VerifiedJoinVersion == "" || cfg.VerifiedJoinVersion != session.Version {
		return nil, fmt.Errorf("conversation-join-unverified")
	}
	if cfg.principal() == "" || cfg.Email != "" && cfg.ServiceAccountID != "" || session.Principal != cfg.principal() || !session.Isolated || session.ID == "" {
		return nil, fmt.Errorf("conversation-ownership-unverified")
	}
	start, err := time.Parse(time.RFC3339Nano, session.StartedAt)
	if err != nil {
		return nil, fmt.Errorf("execution-window-unavailable")
	}
	end, err := time.Parse(time.RFC3339Nano, session.FinishedAt)
	if err != nil || end.Before(start) || end.Sub(start) > 31*24*time.Hour {
		return nil, fmt.Errorf("execution-window-unavailable")
	}
	var parts []accounting.Part
	seen := map[string]bool{}
	expectedTotal, expectedPages, received := -1, -1, 0
	for page := 1; page <= 20; page++ {
		query := map[string]any{"startDate": start.UnixMilli(), "endDate": end.UnixMilli(), "page": page, "pageSize": 1000, "product": "cursor"}
		if cfg.Email != "" {
			query["email"] = cfg.Email
		} else {
			query["serviceAccountId"] = cfg.ServiceAccountID
		}
		body, _ := json.Marshal(query)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("usage-request-invalid")
		}
		request.SetBasicAuth(key, "")
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("usage-api-unavailable")
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		_ = response.Body.Close()
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("admin-api-account-ineligible")
		}
		if response.StatusCode != http.StatusOK || readErr != nil || len(raw) > 2<<20 {
			return nil, fmt.Errorf("usage-api-unavailable")
		}
		var result cursorUsagePage
		if json.Unmarshal(raw, &result) != nil || result.Total < 0 || result.Total > 20000 || result.Pagination.CurrentPage != page || result.Pagination.NumPages < 1 || result.Pagination.NumPages > 20 || result.Pagination.PageSize != 1000 || len(result.Events) > 1000 {
			return nil, fmt.Errorf("usage-pagination-incomplete")
		}
		if page == 1 {
			expectedTotal, expectedPages = result.Total, result.Pagination.NumPages
		}
		if result.Total != expectedTotal || result.Pagination.NumPages != expectedPages || result.Pagination.HasNextPage != (page < expectedPages) {
			return nil, fmt.Errorf("usage-window-changed")
		}
		received += len(result.Events)
		for _, event := range result.Events {
			// Missing conversation identifiers cannot be assigned by time/principal.
			if event.ConversationID != session.ID {
				continue
			}
			if event.UserEmail != cfg.Email && cfg.Email != "" || event.ServiceAccountID != cfg.ServiceAccountID && cfg.ServiceAccountID != "" || event.IsHeadless == nil || !*event.IsHeadless {
				return nil, fmt.Errorf("usage-principal-mismatch")
			}
			stamp, err := strconv.ParseInt(event.Timestamp, 10, 64)
			if err != nil || stamp < start.UnixMilli() || stamp > end.UnixMilli() {
				return nil, fmt.Errorf("usage-window-mismatch")
			}
			// Identical start/model/classification cannot establish disjoint requests.
			identity := event.Timestamp + "\x00" + event.ConversationID + "\x00" + event.Model + "\x00" + event.Kind
			if seen[identity] {
				return nil, fmt.Errorf("usage-events-ambiguous")
			}
			seen[identity] = true
			attributes := map[string]string{"state": "provisional", "kind": event.Kind, "source-url": cursorUsageEndpoint, "request-start-ms": event.Timestamp, "is-headless": "true"}
			if event.IsChargeable != nil {
				attributes["is-chargeable"] = strconv.FormatBool(*event.IsChargeable)
			}
			if event.IsTokenBasedCall != nil {
				attributes["is-token-based-call"] = strconv.FormatBool(*event.IsTokenBasedCall)
			}
			fee, err := cursorCentsUSD(event.CursorTokenFee)
			if err != nil {
				return nil, fmt.Errorf("usage-amount-invalid")
			}
			if fee != nil {
				attributes["included-fee-usd"] = string(*fee)
			}
			if event.TokenUsage != nil && len(event.TokenUsage.Discount) > 0 && string(event.TokenUsage.Discount) != "null" {
				discount, err := accounting.ParseJSONAmount(event.TokenUsage.Discount)
				if err != nil {
					return nil, fmt.Errorf("usage-discount-invalid")
				}
				attributes["discount-percent-off"] = string(discount)
			}
			id := fmt.Sprintf("ledger-%x", sha256.Sum256([]byte(identity)))
			entry := contract.AccountingEntry{ID: id, Provider: "cursor", Model: event.Model}
			add := func(raw json.RawMessage, basis string) error {
				usd, err := cursorCentsUSD(raw)
				if err != nil {
					return err
				}
				if usd == nil {
					return nil
				}
				fidelity := "provider-estimate"
				if basis == "provider-metered" {
					fidelity = "metered"
				}
				entry.Costs = append(entry.Costs, contract.CostMeasure{Currency: "USD", Basis: basis, Amount: contract.Measure[contract.Amount]{Collected: &contract.Reading[contract.Amount]{Value: *usd, Source: "cursor-admin-usage", Fidelity: fidelity, Method: "usage-events-v1", Version: session.Version, Attributes: attributes}}})
				return nil
			}
			if err := add(event.ChargedCents, "provider-metered"); err != nil {
				return nil, fmt.Errorf("usage-amount-invalid")
			}
			if event.TokenUsage != nil {
				if err := add(event.TokenUsage.TotalCents, "model-reference"); err != nil {
					return nil, fmt.Errorf("usage-amount-invalid")
				}
			}
			if len(entry.Costs) == 0 {
				continue
			}
			parts = append(parts, accounting.Part{Scope: cursorLedgerScope, Entry: entry})
		}
		if page == expectedPages {
			if received != expectedTotal {
				return nil, fmt.Errorf("usage-pagination-incomplete")
			}
			if len(parts) == 0 {
				return nil, fmt.Errorf("owned-usage-not-yet-available")
			}
			return parts, nil
		}
	}
	return nil, fmt.Errorf("usage-pagination-incomplete")
}

func recoverCursorCosts(r *adapter.TokenRecorder, cfg cursorBillingConfig, session contract.AccountingSession) {
	key := contract.AccountingKey{Kind: "cost", Currency: "USD", Basis: "provider-metered"}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	parts, err := fetchCursorWindow(ctx, client, cursorUsageEndpoint, os.Getenv("CURSOR_ADMIN_API_KEY"), cfg, session)
	if err != nil {
		_ = r.SetCoverage(key, "unknown", err.Error())
		return
	}
	if err := r.ReplaceAccountingScope(cursorLedgerScope, parts); err != nil {
		_ = r.SetCoverage(key, "partial", "usage-observation-rejected")
		return
	}
	for _, basis := range []string{"provider-metered", "model-reference"} {
		_ = r.SetCoverage(contract.AccountingKey{Kind: "cost", Currency: "USD", Basis: basis}, "partial", "provider-window-provisional")
	}
}
