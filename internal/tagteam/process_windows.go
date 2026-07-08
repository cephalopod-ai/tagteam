//go:build windows

package tagteam

import (
	"os/exec"
	"time"
)

func prepareProcessTree(cmd *exec.Cmd) {
	cmd.WaitDelay = 5 * time.Second
}
