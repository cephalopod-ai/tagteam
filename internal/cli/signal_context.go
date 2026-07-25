package cli

import (
	"context"
	"os/signal"
)

// commandSignalContext lets an interrupted CLI run cancel adapters and persist
// a terminal run record before the process exits.
func commandSignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, commandTerminationSignals()...)
}
