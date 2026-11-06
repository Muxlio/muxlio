package plan

import (
	"fmt"

	"github.com/muxlio/muxlio-core/internal/schedule"
)

// ValidateSchedule verifies the target pin and typed input before persistence.
func ValidateSchedule(d schedule.Definition, p *Plan) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if p == nil || p.Digest != d.WorkflowDigest {
		return fmt.Errorf("schedule target must match the registered immutable workflow digest")
	}
	if err := p.VerifyDigest(); err != nil {
		return err
	}
	if errs := Validate(p); HasErrors(errs) {
		return fmt.Errorf("invalid workflow plan: %v", errs)
	}
	return ValidateInput(p, d.Input)
}
