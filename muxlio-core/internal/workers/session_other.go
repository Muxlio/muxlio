//go:build !unix

package workers

import (
	"context"
	"fmt"
)

func lockSession(context.Context, string, bool) (func(), error) {
	return nil, fmt.Errorf("persistent session locking is unavailable on this platform")
}
