//go:build darwin

package cmd

import (
	"github.com/zanetworker/aimux/internal/terminput"
	"github.com/zanetworker/aimux/internal/terminput/iterm2"
)

// terminalDrivers are the terminals remote serve can type into: iTerm2 on
// macOS. closeAll stops their helpers.
func terminalDrivers() (drivers []terminput.Driver, closeAll func()) {
	d := iterm2.New(iterm2.Options{})
	return []terminput.Driver{d}, func() { _ = d.Close() }
}
