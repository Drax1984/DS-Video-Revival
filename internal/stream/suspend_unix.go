//go:build !windows

package stream

import (
	"os"
	"syscall"
)

// suspend hält einen Prozess an (true) oder lässt ihn weiterlaufen (false).
func suspend(p *os.Process, on bool) error {
	if on {
		return p.Signal(syscall.SIGSTOP)
	}
	return p.Signal(syscall.SIGCONT)
}
