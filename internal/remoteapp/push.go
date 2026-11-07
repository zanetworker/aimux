package remoteapp

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/push"
	"github.com/zanetworker/aimux/internal/remoteapi"
	"github.com/zanetworker/aimux/internal/waiting"
)

// pushSetup is what remote serve notifies approvals through.
type pushSetup struct {
	notifiers []push.Notifier
	devices   *push.DeviceStore // set when APNs is on: the app registers here
	clickURL  string            // web client a tapped ntfy notification opens
}

// buildPush turns remote_access.push into senders, refusing a half-done
// setup at startup rather than failing on the first approval.
func buildPush(cfg config.PushConfig, hosts, allowedLogins []string, home string, getenv func(string) string) (pushSetup, error) {
	var p pushSetup
	if n := cfg.Ntfy; n.Topic != "" {
		server := n.Server
		if server == "" {
			server = "https://ntfy.sh"
		}
		token := ""
		if n.TokenEnv != "" {
			if token = getenv(n.TokenEnv); token == "" {
				return p, fmt.Errorf("remote_access.push.ntfy.token_env names %s, which is not set", n.TokenEnv)
			}
		}
		p.notifiers = append(p.notifiers, &push.Ntfy{Server: server, Topic: n.Topic, Token: token})
		p.clickURL = n.ClickURL
		if p.clickURL == "" && len(hosts) > 0 {
			p.clickURL = "https://" + hosts[0] + ":8443/app/"
		}
	}
	if a := cfg.APNs; a != (config.APNsConfig{}) {
		var missing []string
		for _, f := range []struct{ name, v string }{{"key_file", a.KeyFile}, {"key_id", a.KeyID}, {"team_id", a.TeamID}, {"topic", a.Topic}} {
			if f.v == "" {
				missing = append(missing, f.name)
			}
		}
		if len(missing) > 0 {
			return p, fmt.Errorf("remote_access.push.apns needs %s", strings.Join(missing, ", "))
		}
		keyFile := a.KeyFile
		if rest, ok := strings.CutPrefix(keyFile, "~/"); ok {
			keyFile = filepath.Join(home, rest)
		}
		key, err := push.LoadAPNsKey(keyFile)
		if err != nil {
			return p, err
		}
		p.devices = &push.DeviceStore{Path: filepath.Join(home, ".aimux", "remote-devices.json")}
		p.notifiers = append(p.notifiers, &push.APNs{Key: key, KeyID: a.KeyID, TeamID: a.TeamID, Topic: a.Topic, Devices: p.devices, AllowedLogins: allowedLogins})
	}
	return p, nil
}

// titleLookupTimeout bounds reading the session's title, so a slow read of
// a large session leaves the delivery its own time.
const titleLookupTimeout = 5 * time.Second

// lookupTitle reads the session's title, giving up after
// titleLookupTimeout even when the read ignores its context (the session
// reader does): the alert then goes out without it.
func lookupTitle(sessionTitle func(ctx context.Context, id string) string, id string) string {
	ctx, cancel := context.WithTimeout(context.Background(), titleLookupTimeout)
	defer cancel()
	got := make(chan string, 1) // buffered: a late read finishes and is dropped
	go func() { got <- sessionTitle(ctx, id) }()
	select {
	case t := <-got:
		return t
	case <-ctx.Done():
		return ""
	}
}

// approvalHooks are the broker's OnApproval and OnApprovalResolved: the
// first names the session and sends the notification, the second takes it
// back once the approval is settled. The broker runs them in goroutines of
// their own, so an approval answered quickly can be resolved before its
// alert is out: the alert is then skipped, or taken back only after it was
// sent. Failures are logged. Both are nil when nothing is on.
func approvalHooks(p pushSetup, sessionTitle func(ctx context.Context, id string) string, errOut io.Writer) (asked, resolved func(remoteapi.ApprovalNotice)) {
	if len(p.notifiers) == 0 {
		return nil, nil
	}
	type alert struct {
		resolved bool
		sent     bool
		done     chan struct{} // closed once asked has sent or skipped the alert
	}
	var mu sync.Mutex
	alerts := map[string]*alert{}
	get := func(id string) *alert { // under mu
		a, ok := alerts[id]
		if !ok {
			a = &alert{done: make(chan struct{})}
			alerts[id] = a
		}
		return a
	}
	asked = func(n remoteapi.ApprovalNotice) {
		mu.Lock()
		a := get(n.ApprovalID)
		mu.Unlock()
		defer close(a.done)
		title := lookupTitle(sessionTitle, n.Session)
		mu.Lock()
		skip := a.resolved
		a.sent = !skip
		mu.Unlock()
		if skip {
			return // answered or expired already: nothing to look at
		}
		m := push.ForApproval(title, n.Title)
		m.ApprovalID, m.SessionID, m.Expires = n.ApprovalID, n.Session, n.Expires
		if p.clickURL != "" {
			m.Link = p.clickURL + "#/s/" + url.PathEscape(n.Session)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := push.Send(ctx, p.notifiers, m); err != nil {
			_, _ = fmt.Fprintln(errOut, "approval notification:", err)
		}
	}
	resolved = func(n remoteapi.ApprovalNotice) {
		mu.Lock()
		a := get(n.ApprovalID)
		a.resolved = true
		mu.Unlock()
		select {
		case <-a.done:
		case <-time.After(titleLookupTimeout + 40*time.Second): // asked is bounded well inside this
		}
		mu.Lock()
		sent := a.sent
		delete(alerts, n.ApprovalID)
		mu.Unlock()
		if !sent {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := push.Retract(ctx, p.notifiers, push.Message{ApprovalID: n.ApprovalID, SessionID: n.Session}); err != nil {
			_, _ = fmt.Fprintln(errOut, "approval notification (taking it back):", err)
		}
	}
	return asked, resolved
}

// pushSummary names the senders that are on, for the startup banner.
func pushSummary(cfg config.PushConfig) string {
	var on []string
	if cfg.Ntfy.Topic != "" {
		on = append(on, "ntfy topic "+cfg.Ntfy.Topic)
	}
	if cfg.APNs.Topic != "" {
		on = append(on, "APNs for "+cfg.APNs.Topic)
	}
	return strings.Join(on, ", ")
}

// waitingPollEvery is how often remote serve reads the waiting markers.
const waitingPollEvery = 2 * time.Second

// waitingAlerts is the push loop's handler for what Tracker.Poll found: one
// notification per new marker, worded like a phone approval, taken back
// when the marker goes. Nil when nothing is on.
func waitingAlerts(p pushSetup, sessionTitle func(ctx context.Context, id string) string, errOut io.Writer) func(appeared, gone []waiting.Entry) {
	if len(p.notifiers) == 0 {
		return nil
	}
	// Stays under APNs' 64-byte collapse id: "w-" + a UUID + "-" + 13 digits.
	id := func(e waiting.Entry) string {
		return "w-" + e.SessionID + "-" + strconv.FormatInt(e.Since.UnixMilli(), 10)
	}
	return func(appeared, gone []waiting.Entry) {
		for _, e := range gone {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := push.Retract(ctx, p.notifiers, push.Message{ApprovalID: id(e), SessionID: e.SessionID}); err != nil {
				_, _ = fmt.Fprintln(errOut, "permission notification (taking it back):", err)
			}
			cancel()
		}
		for _, e := range appeared {
			m := push.ForApproval(lookupTitle(sessionTitle, e.SessionID), e.Message)
			m.ApprovalID, m.SessionID = id(e), e.SessionID
			if p.clickURL != "" {
				m.Link = p.clickURL + "#/s/" + url.PathEscape(e.SessionID)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := push.Send(ctx, p.notifiers, m); err != nil {
				_, _ = fmt.Fprintln(errOut, "permission notification:", err)
			}
			cancel()
		}
	}
}

// runWaitingAlerts polls the markers until ctx ends, handing changes to
// alerts (which may be nil: the list still shows waiting sessions).
func runWaitingAlerts(ctx context.Context, tracker *waiting.Tracker, alerts func(appeared, gone []waiting.Entry)) {
	if alerts == nil {
		return
	}
	t := time.NewTicker(waitingPollEvery)
	defer t.Stop()
	for {
		appeared, gone := tracker.Poll()
		if len(appeared)+len(gone) > 0 {
			alerts(appeared, gone)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
