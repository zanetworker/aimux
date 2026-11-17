package remoteapi

import (
	"context"
	"encoding/base64"
	"strings"
	"sync"
	"testing"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/profilerun"
)

// pngB64 is base64 of n bytes that start with the PNG signature.
func pngB64(n int) string {
	b := make([]byte, n)
	copy(b, "\x89PNG\r\n\x1a\n")
	return base64.StdEncoding.EncodeToString(b)
}

func imageBody(mime, data, page string) string {
	return `{"profile":"page","url":"https://e.example/a","text":"what is this?","pageText":"` + page +
		`","image":{"mimeType":"` + mime + `","data":"` + data + `"}}`
}

// An image is checked before anything starts: a mime type other than png,
// jpeg, webp or gif, data that is not base64, bytes that are not that type,
// or more than 1.5 MiB of base64 is 400 image_invalid. An image at the cap
// beside a page text at its own cap (every byte JSON-escaped to six) fits
// the body limit, and the engine sees it.
func TestNewSession_ImageValidated(t *testing.T) {
	r := newProfileRig(t)
	var mu sync.Mutex
	var got []*acprun.Image
	r.f.sendF = func(ctx context.Context, _, _ string, _ func(acprun.Event)) (string, error) {
		mu.Lock()
		got = append(got, profilerun.ImageFrom(ctx))
		mu.Unlock()
		return "end_turn", nil
	}
	r.serve()
	small := pngB64(64)
	r.expectRefused(imageBody("image/svg+xml", small, ""), 400, "image_invalid")
	r.expectRefused(imageBody("", small, ""), 400, "image_invalid")
	r.expectRefused(imageBody("image/png", "not base64!", ""), 400, "image_invalid")
	r.expectRefused(imageBody("image/png", "data:image/png;base64,"+small, ""), 400, "image_invalid")
	r.expectRefused(imageBody("image/png", "", ""), 400, "image_invalid")
	r.expectRefused(imageBody("image/jpeg", small, ""), 400, "image_invalid") // PNG bytes, JPEG label
	r.expectRefused(imageBody("image/png", base64.StdEncoding.EncodeToString([]byte("<svg onload=alert(1)>")), ""), 400, "image_invalid")
	over := pngB64(maxImageBase64/4*3 + 3)
	if len(over) <= maxImageBase64 {
		t.Fatal("over is not over the cap")
	}
	r.expectRefused(imageBody("image/png", over, ""), 400, "image_invalid")

	atCap := pngB64(maxImageBase64 / 4 * 3)
	if len(atCap) != maxImageBase64 || maxImageBase64 != 1_572_864 {
		t.Fatalf("cap %d, image %d", maxImageBase64, len(atCap))
	}
	page := strings.Repeat(`\u0001`, maxPageText)
	if resp, out := r.do("POST", "/v1/sessions", imageBody("image/png", atCap, page)); resp.StatusCode != 202 {
		t.Fatalf("image at the cap with a page at its cap: %d %v", resp.StatusCode, out)
	}
	r.idle("p-1")
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] == nil || got[0].MimeType != "image/png" || got[0].Data != atCap {
		t.Fatalf("engine saw image %v", got)
	}
}
