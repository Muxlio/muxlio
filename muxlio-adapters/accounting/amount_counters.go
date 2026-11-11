package accounting

import (
	"fmt"
	"github.com/muxlio/muxlio-adapters/contract"
)

type amountBaseline struct {
	generation    string
	initial, last contract.Amount
}

// AmountCounterTracker attributes only the monotonic change since an explicitly
// established pre-execution baseline for the same source scope and generation.
type AmountCounterTracker struct{ baselines map[string]amountBaseline }

func NewAmountCounterTracker() *AmountCounterTracker {
	return &AmountCounterTracker{baselines: map[string]amountBaseline{}}
}
func (c *AmountCounterTracker) Baseline(scope, generation string, value contract.Amount) error {
	if scope == "" || generation == "" {
		return fmt.Errorf("counter baseline identity required")
	}
	if err := value.Validate(); err != nil {
		return err
	}
	c.baselines[scope] = amountBaseline{generation, value, value}
	return nil
}
func (c *AmountCounterTracker) SinceBaseline(scope, generation string, value contract.Amount) (contract.Amount, bool, error) {
	if err := value.Validate(); err != nil {
		return "", false, err
	}
	b, ok := c.baselines[scope]
	if !ok || b.generation != generation {
		return "", false, nil
	}
	if _, err := SubtractAmount(value, b.last); err != nil {
		delete(c.baselines, scope)
		return "", false, err
	}
	delta, err := SubtractAmount(value, b.initial)
	if err != nil {
		return "", false, err
	}
	b.last = value
	c.baselines[scope] = b
	return delta, true, nil
}
