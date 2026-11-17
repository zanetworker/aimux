//go:build !darwin

package keyring

import (
	"errors"
	"testing"
)

func TestStore_Unsupported(t *testing.T) {
	if err := Store("aimux-mcp-proxy/x", "v"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Store = %v", err)
	}
	if err := Delete("aimux-mcp-proxy/x"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Delete = %v", err)
	}
}
