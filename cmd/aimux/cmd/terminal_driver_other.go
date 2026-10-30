//go:build !darwin

package cmd

import "github.com/zanetworker/aimux/internal/terminput"

// terminalDrivers: no terminal driver off macOS, so every attempt answers
// no_terminal_driver.
func terminalDrivers() (drivers []terminput.Driver, closeAll func()) {
	return nil, func() {}
}
