package remoteapi

import (
	"testing"

	"github.com/zanetworker/aimux/internal/remoteapi/chatqueue"
)

// A queued item must fit the terminal send path it may go through.
func TestQueueTextCapIsTheTerminalMessageCap(t *testing.T) {
	if chatqueue.MaxTextBytes != maxTerminalBody {
		t.Fatalf("chatqueue.MaxTextBytes = %d, maxTerminalBody = %d", chatqueue.MaxTextBytes, maxTerminalBody)
	}
}
