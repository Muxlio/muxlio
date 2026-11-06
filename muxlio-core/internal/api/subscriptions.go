package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/subscription"
)

func registerSubscriptions(mux *http.ServeMux, store state.Store) {
	management := func(w http.ResponseWriter, r *http.Request) {
		s, ok := store.(subscription.Store)
		if !ok {
			writeError(w, 503, CodeStoreError, "durable subscription storage unavailable")
			return
		}
		if r.URL.Path == "/v1/subscriptions" {
			switch r.Method {
			case http.MethodGet:
				items, err := s.ListSubscriptions()
				if err != nil {
					writeError(w, 500, CodeStoreError, "could not list subscriptions")
					return
				}
				writeJSON(w, 200, map[string]any{"subscriptions": items})
			case http.MethodPost:
				var req struct {
					subscription.Subscription
					Secret     string `json:"secret"`
					Enabled    *bool  `json:"enabled"`
					MaxRetries *int   `json:"maxRetries"`
				}
				dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
				dec.DisallowUnknownFields()
				if err := dec.Decode(&req); err != nil {
					writeError(w, 400, CodeInvalidJSON, "invalid subscription JSON")
					return
				}
				var extra any
				if dec.Decode(&extra) != io.EOF {
					writeError(w, 400, CodeInvalidJSON, "expected exactly one JSON value")
					return
				}
				sub := req.Subscription
				sub.Secret = req.Secret
				sub.Enabled = req.Enabled == nil || *req.Enabled
				sub.MaxRetries = subscription.DefaultMaxRetries
				if req.MaxRetries != nil {
					sub.MaxRetries = *req.MaxRetries
				}
				if err := sub.Normalize(); err != nil {
					writeError(w, 400, CodeBadRequest, err.Error())
					return
				}
				if err := s.CreateSubscription(sub); err != nil {
					writeError(w, 400, CodeBadRequest, "subscription already exists or cannot be stored")
					return
				}
				writeJSON(w, 201, sub)
			default:
				w.Header().Set("Allow", "GET, POST")
				writeError(w, 405, CodeMethodNotAllowed, "use GET or POST")
			}
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/subscriptions/"), "/")
		if len(parts) == 1 && parts[0] == "deadletters" {
			if r.Method != http.MethodGet {
				writeError(w, 405, CodeMethodNotAllowed, "use GET")
				return
			}
			items, err := s.ListDeadLetters(1000)
			if err != nil {
				writeError(w, 500, CodeStoreError, "could not list dead letters")
				return
			}
			writeJSON(w, 200, map[string]any{"deliveries": items})
			return
		}
		if len(parts) == 3 && parts[0] == "deliveries" && parts[2] == "requeue" {
			if r.Method != http.MethodPost {
				writeError(w, 405, CodeMethodNotAllowed, "use POST")
				return
			}
			if err := s.RequeueDeadLetter(parts[1], time.Now().UTC()); err != nil {
				writeError(w, 400, CodeBadRequest, "dead letter unavailable")
				return
			}
			writeJSON(w, 200, map[string]any{"requeued": true})
			return
		}
		if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] != "deliveries") {
			writeError(w, 404, CodeNotFound, "unknown subscription endpoint")
			return
		}
		if r.Method != http.MethodGet && !(r.Method == http.MethodDelete && len(parts) == 1) {
			writeError(w, 405, CodeMethodNotAllowed, "use GET or DELETE")
			return
		}
		sub, err := s.GetSubscription(parts[0])
		if err != nil {
			writeError(w, 404, CodeNotFound, "subscription not found")
			return
		}
		if r.Method == http.MethodDelete {
			if err := s.DeleteSubscription(sub.ID); err != nil {
				writeError(w, 500, CodeStoreError, "could not disable subscription")
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if len(parts) == 1 {
			writeJSON(w, 200, sub)
			return
		}
		items, err := s.ListSubscriptionDeliveries(sub.ID, 1000)
		if err != nil {
			writeError(w, 500, CodeStoreError, "could not list deliveries")
			return
		}
		writeJSON(w, 200, map[string]any{"deliveries": items})
	}
	mux.HandleFunc("/v1/subscriptions", management)
	mux.HandleFunc("/v1/subscriptions/", management)
}
