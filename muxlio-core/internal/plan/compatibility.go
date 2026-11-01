package plan

import "fmt"

// CheckCompatibility reports whether this coordinator can interpret a plan's
// IR. Compatibility is deliberately explicit: a matching major number alone
// is not sufficient because a minor release may add semantics this binary
// does not implement.
func CheckCompatibility(irVersion string) error {
	if irVersion != CurrentIRVersion {
		return fmt.Errorf("muxlio: unsupported plan IR version %q (supported: %s)", irVersion, CurrentIRVersion)
	}
	return nil
}
