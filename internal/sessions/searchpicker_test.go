package sessions

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/search"
)

func TestFormatRow_SingleLineWithHiddenID(t *testing.T) {
	r := search.Result{SessionID: "a97a2cca-2fba", CWD: "/Users/me/go/src/OpenShell", Title: "openshell-service-accounts", ModTime: time.Now().Add(-2 * time.Hour)}

	row := FormatRow(r, true)
	if strings.Contains(row, "\n") {
		t.Fatalf("row spans lines: %q", row)
	}
	plain := ansiRegexp.ReplaceAllString(row, "")
	fields := strings.Split(plain, "\t")
	if len(fields) != 2 || fields[0] != "a97a2cca-2fba" {
		t.Fatalf("want id<TAB>display, got %q", fields)
	}
	for _, want := range []string{"●", "openshell-service-accounts", "OpenShell", "2h"} {
		if !strings.Contains(fields[1], want) {
			t.Errorf("display %q missing %q", fields[1], want)
		}
	}
	if ParseSelectedID(row) != "a97a2cca-2fba" {
		t.Errorf("ParseSelectedID(row) = %q", ParseSelectedID(row))
	}
	if strings.Contains(ansiRegexp.ReplaceAllString(FormatRow(r, false), ""), "●") {
		t.Error("not-live row shows the live marker")
	}
}

func TestFormatRow_LongTitleTruncatedAndUntitledFallback(t *testing.T) {
	long := search.Result{SessionID: "x", Title: strings.Repeat("t", 200), CWD: "/p", ModTime: time.Now()}
	if n := len([]rune(ansiRegexp.ReplaceAllString(FormatRow(long, false), ""))); n > 90 {
		t.Errorf("row is %d runes; long titles must be cut", n)
	}
	untitled := search.Result{SessionID: "y", CWD: "/p", ModTime: time.Now()}
	if !strings.Contains(FormatRow(untitled, false), "(untitled)") {
		t.Error("empty title should render as (untitled)")
	}
}

func TestPickerState_TogglesPersist(t *testing.T) {
	st := PickerState{Dir: t.TempDir()}
	if st.IncludeAutomated() || st.Mode() != "keyword" {
		t.Fatalf("defaults: automated=%v mode=%s, want hidden + keyword (fast while typing)", st.IncludeAutomated(), st.Mode())
	}
	if err := st.Toggle("automated"); err != nil {
		t.Fatal(err)
	}
	if err := st.Toggle("semantic"); err != nil {
		t.Fatal(err)
	}
	again := PickerState{Dir: st.Dir} // a separate process reads the same dir
	if !again.IncludeAutomated() || again.Mode() != "hybrid" {
		t.Errorf("after toggle: automated=%v mode=%s", again.IncludeAutomated(), again.Mode())
	}
	if err := again.Toggle("semantic"); err != nil {
		t.Fatal(err)
	}
	if st.Mode() != "keyword" {
		t.Error("second toggle should switch back to keyword")
	}
	if err := st.Toggle("bogus"); err == nil {
		t.Error("unknown toggle: want error")
	}
}

func TestPickerState_Live(t *testing.T) {
	st := PickerState{Dir: t.TempDir()}
	if err := st.SetLive([]string{"a97a2cca", "43ce13d4"}); err != nil {
		t.Fatal(err)
	}
	live := PickerState{Dir: st.Dir}.Live()
	if !live["a97a2cca"] || !live["43ce13d4"] || live["other"] {
		t.Errorf("Live() = %v", live)
	}
	if len((PickerState{Dir: t.TempDir()}).Live()) != 0 {
		t.Error("no live file: want empty set")
	}
}

func TestPickerHeader(t *testing.T) {
	st := PickerState{Dir: t.TempDir()}
	h := st.Header()
	for _, want := range []string{"keyword", "automated hidden", "enter", "^s", "^a", "^/"} {
		if !strings.Contains(h, want) {
			t.Errorf("header %q missing %q", h, want)
		}
	}
	if err := st.Toggle("automated"); err != nil {
		t.Fatal(err)
	}
	if err := st.Toggle("semantic"); err != nil {
		t.Fatal(err)
	}
	if h := st.Header(); !strings.Contains(h, "semantic") || !strings.Contains(h, "automated shown") {
		t.Errorf("header after toggles: %q", h)
	}
}

func TestFzfArgs_WiresLiveSearchAndPreview(t *testing.T) {
	args := strings.Join(FzfArgs("/opt/my tools/aimux", "Ying deck", "hybrid · automated hidden", 0), "\n")
	for _, want := range []string{
		"--disabled",        // the index ranks; fzf must not re-filter
		"--with-nth=2..",    // hide the id column
		"--query=Ying deck", // start from the typed query
		"start:reload(",     // fill on open
		"change:reload(",    // re-query on every keystroke
		")+first",           // and jump to the best match
		"ctrl-s:transform-header(", "ctrl-a:transform-header(",
		"ctrl-/:toggle-preview",
		"--preview=",
		"'/opt/my tools/aimux'", // binary path with spaces is quoted
	} {
		if !strings.Contains(args, want) {
			t.Errorf("fzf args missing %q", want)
		}
	}
}

func TestFormatDetail_Sections(t *testing.T) {
	d := search.Detail{
		SessionID: "a97a2cca", CWD: "/Users/me/OpenShell", Title: "openshell-service-accounts",
		FirstPrompt: "tell me what is on my cluster", Exchanges: 109, ModTime: time.Now().Add(-time.Hour),
		RecentPrompts: []string{"which sessions built agent-ops", "retitle the badge to agent-ops"},
	}
	out := ansiRegexp.ReplaceAllString(FormatDetail(d, "we need [short]-[lived] tokens", true), "")
	for _, want := range []string{"openshell-service-accounts", "/Users/me/OpenShell", "a97a2cca", "live", "109 exchanges",
		"MATCH", "short", "STARTED WITH", "tell me what is on my cluster", "RECENTLY", "retitle the badge"} {
		if !strings.Contains(out, want) {
			t.Errorf("preview missing %q:\n%s", want, out)
		}
	}
	// no query: no MATCH section
	if strings.Contains(ansiRegexp.ReplaceAllString(FormatDetail(d, "", false), ""), "MATCH") {
		t.Error("MATCH section shown without a match")
	}
}

func TestFzfArgs_ResponsivePreviewAndListenPort(t *testing.T) {
	args := strings.Join(FzfArgs("/bin/aimux", "", "h", 43210), "\n")
	if !strings.Contains(args, "--listen=127.0.0.1:43210") {
		t.Error("missing --listen for background reloads")
	}
	if !strings.Contains(args, "<110(down") {
		t.Error("preview should move below the list in narrow windows")
	}
	if strings.Contains(strings.Join(FzfArgs("/bin/aimux", "", "h", 0), "\n"), "--listen") {
		t.Error("port 0 means no listen flag")
	}
}

func TestPostAction(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}))
	defer srv.Close()
	port, _ := strconv.Atoi(srv.URL[strings.LastIndex(srv.URL, ":")+1:])

	if err := postAction(port, "reload(x)"); err != nil || body != "reload(x)" {
		t.Errorf("postAction: err=%v body=%q", err, body)
	}
	srv.Close()
	if err := postAction(port, "reload(x)"); err == nil {
		t.Error("closed port: want error")
	}
}

func TestPickerState_NoDirNeverWritesFiles(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	st := PickerState{}
	if err := st.Toggle("automated"); err == nil {
		t.Error("toggle without a state dir: want error")
	}
	if err := st.SetLive([]string{"x"}); err == nil {
		t.Error("SetLive without a state dir: want error")
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Errorf("wrote %d files into the working directory", len(entries))
	}
}

func TestColors_ProjectStableAndAgeByRecency(t *testing.T) {
	if first, again := projectColor("OpenShell"), projectColor("OpenShell"); first != again {
		t.Error("project color must be stable")
	}
	seen := map[string]bool{}
	for _, p := range []string{"OpenShell", "research", "aimux", "llama-stack", "openclaw", "blog-concept"} {
		seen[projectColor(p)] = true
	}
	if len(seen) < 3 {
		t.Errorf("projects should get varied colors, got %d distinct", len(seen))
	}
	now := time.Now()
	fresh, today, old := ageColor(now.Add(-20*time.Minute)), ageColor(now.Add(-5*time.Hour)), ageColor(now.Add(-40*24*time.Hour))
	if fresh == today || today == old || fresh == old {
		t.Errorf("age colors should differ: %q %q %q", fresh, today, old)
	}
	row := FormatRow(search.Result{SessionID: "x", Title: "t", CWD: "/a/OpenShell", ModTime: now}, false)
	if !strings.Contains(row, projectColor("OpenShell")) {
		t.Error("row does not use the project color")
	}
}

func TestPickerHeader_MentionsDoubleClick(t *testing.T) {
	if !strings.Contains(PickerState{Dir: t.TempDir()}.Header(), "double-click") {
		t.Error("header should say rows open on enter or double-click")
	}
}

func TestPickerState_SetMode(t *testing.T) {
	st := PickerState{Dir: t.TempDir()}
	if err := st.SetMode("hybrid"); err != nil || st.Mode() != "hybrid" {
		t.Errorf("SetMode hybrid: mode=%s err=%v", st.Mode(), err)
	}
	if err := st.SetMode("fuzzy"); err == nil {
		t.Error("invalid mode: want error")
	}
}

func TestFzfArgs_DebouncesTyping(t *testing.T) {
	args := strings.Join(FzfArgs("/bin/aimux", "", "h", 0), "\n")
	if !strings.Contains(args, "change:reload(sleep 0.15;") {
		t.Error("typing should debounce: fzf cancels the pending reload on the next keystroke")
	}
}
