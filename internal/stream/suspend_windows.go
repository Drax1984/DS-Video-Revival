//go:build windows

package stream

import "os"

// suspend: unter Windows nicht unterstützt (FFmpeg läuft durch).
func suspend(p *os.Process, on bool) error { return nil }
