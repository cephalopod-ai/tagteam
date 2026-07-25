//go:build windows

package cli

import "os"

func commandTerminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
