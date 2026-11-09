package subscription

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Hook receives the same signed, redacted bytes as a webhook. Hooks must honor
// context cancellation and are registered by the embedding process, never by
// executing commands supplied through the management API.
type Hook func(context.Context, []byte, string) error

// Dispatcher is owned by one coordinator process. Only one Run or Tick may
// execute at a time. Hooks must be configured before starting the dispatcher.
type Dispatcher struct {
	Store    Store
	Events   EventReader
	Hooks    map[string]Hook
	hookBusy chan struct{}
}

// Run delivers independently of workflow execution. Persistence failures are
// retried on the next poll; cursors advance only after durable enqueue.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		_ = d.Tick(ctx, time.Now().UTC())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) Tick(ctx context.Context, now time.Time) error {
	started := time.Now()
	subs, err := d.Store.ListSubscriptions()
	if err != nil {
		return err
	}
	if len(subs) == 0 {
		return nil
	}
	runs, err := d.Events.ListRuns()
	if err != nil {
		return err
	}
	for _, sub := range subs {
		if !sub.Enabled {
			continue
		}
		for _, run := range runs {
			if err := ctx.Err(); err != nil {
				return err
			}
			key := sub.ID + ":" + run.ID
			after, err := d.Store.GetCursor(key)
			if err != nil {
				return err
			}
			events, err := d.Events.GetEvents(run.ID, after, 100)
			if err != nil {
				return err
			}
			for _, event := range events {
				if sub.Matches(event.Type) {
					// Redact before persistence as well as before transport. Never store an
					// outbound copy of unredacted workflow inputs or worker output.
					record := Delivery{ID: DeliveryID(sub.ID, run.ID, event.Seq), SubscriptionID: sub.ID, RunID: run.ID, EventSeq: event.Seq, EventID: EventID(run.ID, event.Seq), EventType: event.Type, SubjectID: event.SubjectID, Timestamp: event.Timestamp, Payload: RedactPayload(event.Payload, sub.RedactFields), State: StatePending, MaxRetries: sub.MaxRetries, CreatedAt: now}
					if err := d.Store.EnqueueDelivery(record); err != nil {
						return err
					}
				}
				if err := d.Store.SetCursor(key, event.Seq); err != nil {
					return err
				}
			}
		}
	}
	due, err := d.Store.ListDueDeliveries(now, 32)
	if err != nil {
		return err
	}
	for _, record := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		sub, err := d.Store.GetSubscription(record.SubscriptionID)
		if err != nil {
			return err
		}
		if !sub.Enabled {
			continue
		}
		err = d.deliver(ctx, sub, record)
		// Shutdown is not a receiver failure. Leave the delivery pending for
		// replay, including when acknowledgement raced cancellation.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// A stuck hook from an earlier cancelled attempt holds the single local
		// slot; that is internal contention, not a receiver failure.
		if errors.Is(err, ErrLocalHookBusy) {
			continue
		}
		// Include fanout and earlier attempts in this batch, while preserving
		// the caller's clock (which can be synthetic in tests).
		finished := now.Add(time.Since(started))
		next := finished.Add(time.Second * time.Duration(1<<min(record.Attempts, 8)))
		if err := d.Store.RecordSubscriptionDelivery(record.ID, DeliveryResult{Succeeded: err == nil}, &next, finished); err != nil {
			return err
		}
	}
	return nil
}

func (d *Dispatcher) deliver(ctx context.Context, sub Subscription, record Delivery) (err error) {
	// A faulty extension cannot terminate the coordinator.
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("local hook panicked")
		}
	}()
	body, sig, err := BuildEnvelope(sub, record)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if sub.IsLocalHook() {
		hook := d.Hooks[sub.LocalHookName()]
		if hook == nil {
			return fmt.Errorf("local hook unavailable")
		}
		// Keep at most one local hook invocation outstanding, even if a buggy
		// extension ignores cancellation. Its timeout must not stall delivery or
		// coordinator shutdown, and retries must not leak additional goroutines.
		if d.hookBusy == nil {
			d.hookBusy = make(chan struct{}, 1)
		}
		select {
		case d.hookBusy <- struct{}{}:
		default:
			return ErrLocalHookBusy
		}
		done := make(chan error, 1)
		go func() {
			var hookErr error
			defer func() {
				if recover() != nil {
					hookErr = fmt.Errorf("local hook panicked")
				}
				<-d.hookBusy
				done <- hookErr
			}()
			hookErr = hook(ctx, body, sig)
		}()
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(sub.SignatureHeader, sig)
	req.Header.Set("X-Muxlio-Event-ID", record.EventID)
	// A redirect must never forward the signed payload to another destination.
	client := http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("receiver rejected delivery")
	}
	return nil
}
