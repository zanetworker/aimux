package views

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"

	"github.com/zanetworker/aimux/internal/controller"
)

// SystemView is the :system screen: the background services, the model proxy's diagnosis, the engines and roles,
// and the last day's model calls by role. It is what `aimux status`, `aimux model proxy doctor`,
// `aimux engine list` and `aimux cost --by role` print, from one controller.SystemSnapshot, and it only shows.
type SystemView struct {
	snap          *controller.SystemSnapshot
	width, height int
	offset        int // lines scrolled past
	now           func() time.Time
}

func NewSystemView() *SystemView { return &SystemView{now: time.Now} }

// SetSnapshot shows a new reading, keeping the scroll position. Its text comes from files and services, so every
// string in it is made plain first: a control character is never written to the terminal.
func (v *SystemView) SetSnapshot(s controller.SystemSnapshot) {
	s = plainSnapshot(s)
	v.snap = &s
}

// ReadAt is when the reading on the screen was taken; zero when there is none yet.
func (v *SystemView) ReadAt() time.Time {
	if v.snap == nil {
		return time.Time{}
	}
	return v.snap.At
}

// SetNow replaces the clock ages are counted from.
func (v *SystemView) SetNow(now func() time.Time) { v.now = now }

func (v *SystemView) SetSize(w, h int) { v.width, v.height = w, h }

// Scroll moves the screen by lines, down when positive. View keeps it inside the page.
func (v *SystemView) Scroll(lines int) { v.offset += lines }

// View renders the page, cut to the screen's height from the scroll position.
func (v *SystemView) View() string {
	if v.snap == nil {
		return healthDimStyle.Render("  Reading this machine...")
	}
	lines := strings.Split(strings.TrimRight(v.page(), "\n"), "\n")
	if v.width > 0 {
		// A table row wider than the screen is cut at its edge; sentences were wrapped when the page was built.
		fit := lipgloss.NewStyle().MaxWidth(v.width)
		for i, line := range lines {
			if lipgloss.Width(line) > v.width {
				lines[i] = fit.Render(line)
			}
		}
	}
	if v.height > 0 && len(lines) > v.height {
		v.offset = max(0, min(v.offset, len(lines)-v.height))
		lines = lines[v.offset : v.offset+v.height]
	} else {
		v.offset = 0
	}
	return strings.Join(lines, "\n")
}

func (v *SystemView) page() string {
	var b strings.Builder
	s, now := v.snap, v.now()
	section := func(title, command string) {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(healthTitleStyle.Render(title) + healthDimStyle.Render("   "+command) + "\n")
	}
	// sentence writes text wrapped to the screen: lead before its first line, the same indent before the rest.
	sentence := func(lead, text string, render func(string) string) {
		indent := strings.Repeat(" ", lipgloss.Width(lead))
		for i, line := range wrapWords(text, v.width-len(indent)) {
			if i > 0 {
				lead = indent
			}
			b.WriteString(lead + render(line) + "\n")
		}
	}
	asIs := func(s string) string { return s }
	unread := func(why string) {
		sentence("  ", "Could not be read: "+why, func(s string) string { return healthFailStyle.Render(s) })
	}
	note := func(text string) { sentence("  ", text, func(s string) string { return healthDimStyle.Render(s) }) }
	header := func(format string, columns ...any) {
		b.WriteString(healthSectionStyle.Render("  "+fmt.Sprintf(format, columns...)) + "\n")
	}

	section("SERVICES", "aimux status")
	header("%-14s %-14s %-22s %-6s %-17s %s", "SERVICE", "STATE", "BUILD", "UP", "ADDRESS", "LAST ACTIVITY")
	for _, svc := range s.Services.Services {
		build, up := controller.BuildNumber(svc.Build), "-"
		if build == "" {
			build = "-"
		}
		if svc.BuildNote != "" {
			build += " (" + svc.BuildNote + ")"
		}
		if svc.StartedAt != nil {
			up = age(now.Sub(*svc.StartedAt))
		}
		activity := svc.ActivityNote
		switch {
		case svc.Reason != "":
			activity = svc.Reason // for a service that is not well, why matters more than when it last worked
		case svc.LastActivity != nil:
			activity = strings.TrimSpace(svc.ActivityNote + " " + age(now.Sub(*svc.LastActivity)) + " ago")
		}
		fmt.Fprintf(&b, "  %-14s %s %-22s %-6s %-17s %s\n", svc.Name, levelCell(string(svc.State), 14), build, up, dash(svc.Address), dash(activity))
	}
	if items := s.Services.Attention(); len(items) > 0 {
		for _, item := range items {
			sentence("  ", item.Problem, asIs)
			if item.Fix != "" {
				sentence("    "+healthDimStyle.Render("fix:")+" ", item.Fix, asIs)
			}
		}
	} else {
		note("Nothing needs attention. This aimux is build " + controller.BuildNumber(s.Services.CLIBuild) + ".")
	}

	section("MODEL PROXY", "aimux model proxy doctor")
	if s.DoctorErr != "" {
		unread(s.DoctorErr)
	} else {
		fmt.Fprintf(&b, "  %s Checked on port %d.\n", controller.ProxyHeadline(s.Doctor.Findings), s.Doctor.Port)
		passing := 0
		for _, f := range s.Doctor.Findings {
			if f.Level == controller.FindingOK {
				passing++
				continue
			}
			sentence("  "+levelCell(f.Level, 5)+" ", f.Detail, asIs)
			if f.Fix != "" {
				sentence("        "+healthDimStyle.Render("what to do:")+" ", f.Fix, asIs)
			}
		}
		if passing > 0 {
			note(fmt.Sprintf("%d %s", passing, map[bool]string{true: "check passes", false: "checks pass"}[passing == 1]))
		}
	}

	section("ENGINES AND ROLES", "aimux engine list")
	if s.EnginesErr != "" {
		unread(s.EnginesErr)
	} else {
		header("%-14s %-12s %-8s %-18s %s", "ENGINE", "IN THE FILE", "RUNNING", "TYPE", "KEY")
		differ, said := false, false
		for _, e := range s.Engines {
			file, running, differs := e.Words()
			differ, said = differ || differs, said || running != "-"
			fmt.Fprintf(&b, "  %-14s %-12s %-8s %-18s %s\n", e.Name, file, running, e.Type, dash(e.KeyAt))
		}
		switch {
		case len(s.Engines) > 0 && !said:
			note(fmt.Sprintf("The inference proxy on port %d did not say which engines it runs: it does not answer, or it is a build from before it said.", s.EnginesPort))
		case differ:
			sentence("  ", "The running proxy and the file differ. To make them match: aimux setup --apply", asIs)
		}
		b.WriteString("\n")
		header("%-14s %-12s %-28s %s", "ROLE", "USES", "MODEL", "ANSWERED BY")
		for _, r := range s.Roles {
			model := dash(r.Model)
			if r.Model == "" && r.Role == "main" {
				model = "chosen in the session"
			}
			fmt.Fprintf(&b, "  %-14s %-12s %-28s %s\n", r.Role, dash(r.Entry), model, strings.TrimSpace(dash(r.Engine)+" "+r.Note))
		}
		if len(s.Subagents) > 0 {
			b.WriteString("\n")
			header("%-28s %-20s %-24s %s", "SUBAGENT", "DEFINED IN", "NAMES THE MODEL", "RUNS ON")
			for _, a := range s.Subagents {
				runs := "the session's model"
				switch {
				case a.RunsOn != "" && a.FromRole:
					runs = a.RunsOn + " (the subagent role)"
				case a.RunsOn != "":
					runs = a.RunsOn
				}
				fmt.Fprintf(&b, "  %-28s %-20s %-24s %s\n", truncate(a.Name, 28), a.DefinedIn, dash(a.Model), runs)
			}
		}
	}

	section("MODEL CALLS", "aimux cost --since 24h --by role")
	switch {
	case s.MeterErr != "":
		unread(s.MeterErr)
	case s.Meter.Files == 0:
		note("No ledger has been written on this machine yet.")
	case len(s.Meter.Rows) == 0 && s.Meter.Unmatched == 0:
		note("No model calls passed the proxy in the last day.")
	}
	if s.MeterErr == "" && len(s.Meter.Rows) > 0 {
		header("%-14s %8s %8s %10s %10s %10s  %s", "ROLE", "CALLS", "FAILED", "INPUT", "CACHED", "OUTPUT", "ENGINES")
		for _, r := range s.Meter.Rows {
			approx := func(n int64) string {
				if r.Estimated {
					return "~" + formatTokens(n)
				}
				return formatTokens(n)
			}
			fmt.Fprintf(&b, "  %-14s %8d %8d %10s %10s %10s  %s\n", dash(r.Key), r.Requests, r.Failed,
				approx(r.InputTokens), approx(r.CachedTokens), approx(r.OutputTokens), dash(strings.Join(r.Engines, ", ")))
		}
	}
	if s.MeterErr == "" && s.Meter.Files > 0 {
		note("Counted from the ledger: calls and tokens, not money.")
		for _, n := range controller.MeterNotes(s.Meter) {
			note(n)
		}
	}
	return b.String()
}

// levelCell is a state or level in its colour, padded to width as plain text would be.
func levelCell(level string, width int) string {
	style := healthDimStyle
	switch level {
	case string(controller.ServiceOK):
		style = healthOKStyle
	case string(controller.ServiceDown), controller.FindingFail:
		style = healthFailStyle
	case string(controller.ServiceDegraded), controller.FindingWarn:
		style = systemWarnStyle
	}
	return style.Render(level) + strings.Repeat(" ", max(0, width-len(level)))
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// age is a duration as its largest unit: 5s, 3m, 2h, 4d.
func age(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%ds", int(d/time.Second))
}

// wrapWords breaks text into lines of at most width columns at its spaces. A word longer than a line stays
// whole on a line of its own, to be cut at the screen's edge. A width too small to wrap to gives one line.
func wrapWords(text string, width int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}
	if width < 10 {
		return []string{strings.Join(words, " ")}
	}
	lines, line := []string{}, words[0]
	for _, word := range words[1:] {
		if lipgloss.Width(line)+1+lipgloss.Width(word) > width {
			lines, line = append(lines, line), word
			continue
		}
		line += " " + word
	}
	return append(lines, line)
}

// plain is s without control characters, each replaced by a space: what was text stays readable, and nothing in
// it can move the cursor, clear the screen or retitle the terminal.
func plain(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func plainAll(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = plain(s)
	}
	return out
}

// plainSnapshot is s with every string that came from a file or a service made plain. The slices are copied:
// the caller's snapshot is left as it was.
func plainSnapshot(s controller.SystemSnapshot) controller.SystemSnapshot {
	s.DoctorErr, s.EnginesErr, s.MeterErr = plain(s.DoctorErr), plain(s.EnginesErr), plain(s.MeterErr)
	s.Services.CLIBuild = plain(s.Services.CLIBuild)
	services := make([]controller.ServiceStatus, len(s.Services.Services))
	for i, svc := range s.Services.Services {
		svc.Name, svc.Reason, svc.Build, svc.BuildNote = plain(svc.Name), plain(svc.Reason), plain(svc.Build), plain(svc.BuildNote)
		svc.Address, svc.ActivityNote, svc.State = plain(svc.Address), plain(svc.ActivityNote), controller.ServiceState(plain(string(svc.State)))
		attention := make([]controller.Attention, len(svc.Attention))
		for j, a := range svc.Attention {
			attention[j] = controller.Attention{Problem: plain(a.Problem), Fix: plain(a.Fix)}
		}
		svc.Attention = attention
		services[i] = svc
	}
	s.Services.Services = services
	findings := make([]controller.ProxyFinding, len(s.Doctor.Findings))
	for i, f := range s.Doctor.Findings {
		findings[i] = controller.ProxyFinding{ID: plain(f.ID), Level: plain(f.Level), Detail: plain(f.Detail), Fix: plain(f.Fix)}
	}
	s.Doctor.Findings = findings
	engines := make([]controller.EngineRow, len(s.Engines))
	for i, e := range s.Engines {
		e.Name, e.Type, e.Auth, e.URL, e.KeyAt = plain(e.Name), plain(e.Type), plain(e.Auth), plain(e.URL), plain(e.KeyAt)
		engines[i] = e
	}
	s.Engines = engines
	roles := make([]controller.RoleRow, len(s.Roles))
	for i, r := range s.Roles {
		roles[i] = controller.RoleRow{Role: plain(r.Role), Entry: plain(r.Entry), Model: plain(r.Model), Engine: plain(r.Engine), Note: plain(r.Note)}
	}
	s.Roles = roles
	subagents := make([]controller.SubagentRow, len(s.Subagents))
	for i, a := range s.Subagents {
		a.Name, a.DefinedIn, a.Model, a.RunsOn = plain(a.Name), plain(a.DefinedIn), plain(a.Model), plain(a.RunsOn)
		subagents[i] = a
	}
	s.Subagents = subagents
	rows := make([]controller.MeterRow, len(s.Meter.Rows))
	for i, r := range s.Meter.Rows {
		r.Key, r.Engines = plain(r.Key), plainAll(r.Engines)
		rows[i] = r
	}
	s.Meter.Rows = rows
	return s
}
