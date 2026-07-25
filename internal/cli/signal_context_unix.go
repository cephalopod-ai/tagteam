//go:build !windows

package cli

import (
	"os"
	"syscall"
)

func commandTerminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
