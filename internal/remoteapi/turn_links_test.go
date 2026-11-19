package remoteapi

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/profilerun"
)

// A turn whose engine reports links (a research-with-work answer) ends
// with them on turn_done; other turns' turn_done has none.
func TestAPI_FinalEventCarriesLinks(t *testing.T) {
	f := &fakeBackend{sendF: func(ctx context.Context, s, text string, on func(acprun.Event)) (string, error) {
		if s == "run" {
			profilerun.SetTurnLinks(ctx, []string{"https://github.com/NVIDIA/OpenShell/releases"})
		}
		return "end_turn", nil
	}}
	b := newBroker(t, f, time.Second)
	for _, tc := range []struct {
		session string
		want    []string
	}{{"run", []string{"https://github.com/NVIDIA/OpenShell/releases"}}, {"plain", nil}} {
		if _, err := b.Start(tc.session, "q", ""); err != nil {
			t.Fatal(err)
		}
		evs := waitFor(t, b, tc.session, KindTurnDone)
		var done struct {
			StopReason string   `json:"stopReason"`
			Links      []string `json:"links"`
		}
		if err := json.Unmarshal(evs[len(evs)-1].Data, &done); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(done.Links, tc.want) {
			t.Errorf("%s: turn_done links %v, want %v (data %s)", tc.session, done.Links, tc.want, evs[len(evs)-1].Data)
		}
	}
}
