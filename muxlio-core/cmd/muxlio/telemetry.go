package main

import (
	"context"
	"time"

	"github.com/muxlio/muxlio-core/internal/telemetry"
)

// Commands return to main before os.Exit, so their deferred shutdown can flush.
func setupTelemetry() (func(), error) {
	shutdown, err := telemetry.Setup(context.Background(), "muxlio")
	if err != nil {
		return nil, err
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdown(ctx)
	}, nil
}
