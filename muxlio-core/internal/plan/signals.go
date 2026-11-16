package plan

import "fmt"

// Signal history is durable and non-destructive. Keep selecting the latest
// delivery, including when a waiting activation resumes after recovery.
func (e *Engine) consumeSignal(runID, name string) (any, bool, error) {
	signals, err := e.store.ConsumeSignals(runID, name)
	if err != nil {
		return nil, false, fmt.Errorf("muxlio: consuming signal %q: %w", name, err)
	}
	if len(signals) == 0 {
		return nil, false, nil
	}
	return signals[len(signals)-1].Payload, true, nil
}
