//go:build !darwin && !linux

package toolgateway

import (
	"errors"
	"net"
)

// peerUID cannot check the peer here, so every connection is refused.
func peerUID(net.Conn) (int, error) {
	return -1, errors.New("peer credentials are not supported on this platform")
}
