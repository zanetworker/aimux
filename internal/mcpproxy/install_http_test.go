package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/jsonedit"
	"github.com/zanetworker/aimux/internal/keyring"
)

// sentinelValue is a header value that must never leave the Keychain.
const sentinelValue = "SENTINEL-hdr-7c1e9"

// memVault is an in-memory VaultStore.
type memVault struct {
	items  map[string]string
	puts   int
	gets   int
	getErr error
	max    int // 0: no limit
}

func (m *memVault) MaxValue(string) int {
	if m.max == 0 {
		return 1 << 20
	}
	return m.max
}

func (m *memVault) Put(service, value string) error {
	if m.items == nil {
		m.items = map[string]string{}
	}
	m.puts++
	m.items[service] = value
	return nil
}

func (m *memVault) Get(_ context.Context, service string) (string, error) {
	m.gets++
	if m.getErr != nil {
		return "", m.getErr
	}
	v, ok := m.items[service]
	if !ok {
		return "", fmt.Errorf("keychain %s: %w", service, keyring.ErrNotFound)
	}
	return v, nil
}

func (m *memVault) Delete(service string) error {
	delete(m.items, service)
	return nil
}

// httpClaude has one stdio server, two movable HTTP servers (one with a
// header), one OAuth server and one that is down.
const httpClaude = `{
  "mcpServers": {
    "slack": {"command": "slack-mcp", "args": []},
    "deepwiki": {"type": "http", "url": "https://mcp.deepwiki.com/mcp"},
    "pp": {"type": "http", "url": "https://pp.example/mcp", "headers": {"Authorization": "Bearer ` + sentinelValue + `"}},
    "granola": {"type": "http", "url": "https://mcp.granola.ai/mcp"},
    "devsrv": {"type": "http", "url": "http://127.0.0.1:3033/mcp"}
  }
}`

type httpFX struct {
	*installFX
	vault   *memVault
	checked []string
	reach   map[string]HTTPReach // by URL; default Reachable
	listed  []string
}

func newHTTPFX(t *testing.T, claude string) *httpFX {
	t.Helper()
	fx := &httpFX{installFX: newInstallFX(t, claude, ""), vault: &memVault{}, reach: map[string]HTTPReach{
		"https://mcp.granola.ai/mcp": NeedsOAuth,
		"http://127.0.0.1:3033/mcp":  Unreachable,
	}}
	fx.in.Vault = fx.vault
	var mu sync.Mutex
	fx.in.CheckHTTP = func(_ context.Context, url string, _ map[string]string) HTTPReach {
		mu.Lock()
		defer mu.Unlock()
		fx.checked = append(fx.checked, url)
		return fx.reach[url]
	}
	fx.in.RefreshHTTP = func(catalogPath string, _ map[string]HTTPEntry, names []string) map[string]error {
		fx.listed = append(fx.listed, names...)
		cat, err := LoadCatalog(catalogPath)
		must(t, err)
		for _, n := range names {
			cat.Set(n, []json.RawMessage{json.RawMessage(`{"name":"search","inputSchema":{"type":"object"}}`)}, time.Now())
		}
		must(t, cat.Save(catalogPath))
		return map[string]error{}
	}
	return fx
}

func TestPlan_MovesStdioAndHTTP(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	p, err := fx.in.Plan(nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Move, ",") != "deepwiki,pp,slack" || strings.Join(p.HTTP, ",") != "deepwiki,pp" {
		t.Fatalf("move %v http %v", p.Move, p.HTTP)
	}
}

func TestPlan_OAuthAndDownServersKeptDirect(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	p, err := fx.in.Plan(nil)
	if err != nil {
		t.Fatal(err)
	}
	if keptReason(p.KeptDirect, "granola") != ReasonNeedsOAuth || keptReason(p.KeptDirect, "devsrv") != reasonUnreachable {
		t.Fatalf("kept %v", p.KeptDirect)
	}
	for _, n := range p.Move {
		if n == "granola" || n == "devsrv" {
			t.Fatalf("%s in Move", n)
		}
	}
}

func TestPlan_MovedAndSkippedNotProbed(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install([]string{"pp"}))
	fx.checked = nil
	if _, err := fx.in.Plan(nil); err != nil {
		t.Fatal(err)
	}
	for _, u := range fx.checked {
		if u == "https://mcp.deepwiki.com/mcp" || u == "https://pp.example/mcp" {
			t.Fatalf("probed %s (moved or skipped)", u)
		}
	}
}

func TestPlan_SkipAcceptsHTTPAndOAuthNames(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	p, err := fx.in.Plan([]string{"pp", "granola"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Skip, ",") != "granola,pp" || keptReason(p.KeptDirect, "granola") != "" {
		t.Fatalf("skip %v kept %v", p.Skip, p.KeptDirect)
	}
}

func TestPlan_SkipUnknownName(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	_, err := fx.in.Plan([]string{"nope"})
	if err == nil || !strings.Contains(err.Error(), "--skip nope: no MCP server by that name in") {
		t.Fatalf("err = %v", err)
	}
}

func TestDryRun_ListsHTTPWithoutVault(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	before, _ := os.ReadFile(fx.catalog)
	p, err := fx.in.DryRun(nil)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(fx.catalog)
	if fx.vault.puts+fx.vault.gets != 0 || string(before) != string(after) {
		t.Fatalf("vault calls %d, catalog changed %v", fx.vault.puts+fx.vault.gets, string(before) != string(after))
	}
	if !strings.Contains(strings.Join(p.Listed, ","), "deepwiki") {
		t.Fatalf("listed %v", p.Listed)
	}
}

func TestPrintPlan_HTTPLines(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	p, err := fx.in.Plan(nil)
	if err != nil {
		t.Fatal(err)
	}
	fx.in.PrintPlan(p)
	out := fx.out.String()
	for _, want := range []string{
		"  deepwiki (http) -> http://127.0.0.1:" + fmt.Sprint(testPort) + "/mcp/deepwiki",
		"Would keep direct: granola: needs OAuth (later)",
		"Would keep direct: devsrv: not reachable now; run install again when it is up",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

// filesUnder returns every regular file under dir.
func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func TestInstall_MovesHTTPEntry(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	_, members, err := readMCPServers(fx.claude)
	must(t, err)
	if !jsonEqual(members["pp"], fx.in.entryFor("pp")) || !jsonEqual(members["deepwiki"], fx.in.entryFor("deepwiki")) {
		t.Fatalf("entries %s %s", members["pp"], members["deepwiki"])
	}
	servers, _, err := LoadServerFile(fx.servers)
	must(t, err)
	if d := servers["pp"]; d.Type != "http" || d.URL != "https://pp.example/mcp" || !reflect.DeepEqual(d.Secrets, keychainHeaderRefs("pp", []string{"Authorization"})) {
		t.Fatalf("pp def %+v", d)
	}
	item, err := decodeHeaderSecret(fx.vault.items[HeaderService("pp")])
	must(t, err)
	if item.Headers["Authorization"].Value != "Bearer "+sentinelValue || item.Headers["Authorization"].literal() != `"Bearer `+sentinelValue+`"` || item.Headers["Authorization"].Literal != "" {
		t.Fatal("item value or literal wrong")
	}
	if _, ok := fx.vault.items[HeaderService("deepwiki")]; ok {
		t.Fatal("headerless server got an item")
	}
	if keptReason(nil, "") != "" || members["granola"] == nil {
		t.Fatal("granola entry should stay")
	}
}

func TestInstall_NoHeaderValueAnywhere(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	if strings.Contains(fx.out.String(), sentinelValue) {
		t.Fatal("stdout has the value")
	}
	for _, f := range filesUnder(t, fx.dir) {
		if f == fx.claude {
			continue
		}
		b, _ := os.ReadFile(f) // #nosec G304 -- a file under the test's temp dir
		if strings.Contains(string(b), sentinelValue) {
			t.Fatalf("%s holds the header value", f)
		}
	}
	for _, c := range fx.lc.calls {
		if strings.Contains(c, sentinelValue) {
			t.Fatal("launchctl argv has the value")
		}
	}
}

func TestInstall_ManifestV3RedactedOriginal(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	m, err := LoadManifest(fx.manifest)
	must(t, err)
	if m.Version != 3 {
		t.Fatalf("version %d", m.Version)
	}
	pp := m.Servers["pp"]
	if !strings.Contains(pp.Original, headerPlaceholder) || pp.Keychain != HeaderService("pp") {
		t.Fatalf("pp %+v", pp)
	}
	dw := m.Servers["deepwiki"]
	if dw.Keychain != "" || dw.Original != `{"type": "http", "url": "https://mcp.deepwiki.com/mcp"}` {
		t.Fatalf("deepwiki %+v", dw)
	}
}

func TestInstall_ProbeFailureDeletesItemsItWrote(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	before, _ := os.ReadFile(fx.claude)
	fx.probeFailing["pp"] = true
	if err := fx.in.Install(nil); err == nil {
		t.Fatal("want error")
	}
	after, _ := os.ReadFile(fx.claude)
	if string(before) != string(after) || len(fx.vault.items) != 0 {
		t.Fatalf("config changed %v, items %d", string(before) != string(after), len(fx.vault.items))
	}
}

func TestInstall_RerunAfterCrashOverwritesItem(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.vault.Put(HeaderService("pp"), "stale"))
	must(t, fx.in.Install(nil))
	if len(fx.vault.items) != 1 || fx.vault.items[HeaderService("pp")] == "stale" {
		t.Fatalf("items %d", len(fx.vault.items))
	}
}

func TestInstall_RerunIsNoOp(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	puts := fx.vault.puts
	before, _ := os.ReadFile(fx.claude)
	fx.out.Reset()
	must(t, fx.in.Install(nil))
	after, _ := os.ReadFile(fx.claude)
	if fx.vault.puts != puts || string(before) != string(after) || !strings.Contains(fx.out.String(), "no new servers to move") {
		t.Fatalf("puts %d->%d changed %v out %s", puts, fx.vault.puts, string(before) != string(after), fx.out.String())
	}
}

func TestInstall_DownServerKeptDirectRestMoves(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	_, members, _ := readMCPServers(fx.claude)
	if jsonEqual(members["devsrv"], fx.in.entryFor("devsrv")) {
		t.Fatal("down server moved")
	}
	if !strings.Contains(fx.out.String(), "kept direct: devsrv: not reachable now") {
		t.Fatalf("out %s", fx.out.String())
	}
	m, _ := LoadManifest(fx.manifest)
	if contains(m.Skipped, "devsrv") {
		t.Fatal("down server recorded as skip")
	}
}

func TestInstall_RecordsNeedsOAuthInManifest(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	m, _ := LoadManifest(fx.manifest)
	if len(m.Direct) != 1 || m.Direct[0].Server != "granola" || m.Direct[0].Reason != ReasonNeedsOAuth {
		t.Fatalf("direct %v", m.Direct)
	}
}

func TestInstall_HTTPEntryChangedDuringInstallRefused(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	fx.in.edit = func(path, key string, fn func(json.RawMessage) (json.RawMessage, error)) error {
		if path == fx.claude {
			b, _ := os.ReadFile(path) // #nosec G304 -- the test's temp config
			changed := strings.Replace(string(b), "https://mcp.deepwiki.com/mcp", "https://other.example/mcp", 1)
			must(t, os.WriteFile(path, []byte(changed), 0o600)) // #nosec G703 -- the test's temp config
		}
		return jsonedit.EditKeyVerbatim(path, key, fn)
	}
	err := fx.in.Install(nil)
	if err == nil || !strings.Contains(err.Error(), "deepwiki changed while installing") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadManifest_ReadsV2AndV3(t *testing.T) {
	dir := t.TempDir()
	for _, v := range []int{2, 3} {
		p := filepath.Join(dir, fmt.Sprintf("m%d.json", v))
		must(t, os.WriteFile(p, []byte(fmt.Sprintf(`{"version":%d,"port":7381,"servers":{}}`, v)), 0o600))
		if m, err := LoadManifest(p); err != nil || m.Version != v {
			t.Fatalf("v%d: %v", v, err)
		}
	}
}

func TestLoadManifest_RefusesV4(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.json")
	must(t, os.WriteFile(p, []byte(`{"version":4,"port":7381,"servers":{}}`), 0o600))
	if _, err := LoadManifest(p); err == nil || !strings.Contains(err.Error(), "written by a newer aimux") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstall_BackupHasNoHeaderValue(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	before, _ := os.ReadFile(fx.claude)
	must(t, fx.in.Install(nil))
	backups := filesUnder(t, fx.backups)
	if len(backups) != 1 {
		t.Fatalf("backups %v", backups)
	}
	b, _ := os.ReadFile(backups[0])
	if strings.Contains(string(b), sentinelValue) {
		t.Fatal("backup holds the header value")
	}
	want := strings.Replace(string(before), `"Bearer `+sentinelValue+`"`, headerPlaceholder, 1)
	if string(b) != want {
		t.Fatalf("backup differs beyond the header:\n%s", b)
	}
}

func TestInstall_NotesOlderBackupsWithHeaders(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, os.MkdirAll(fx.backups, 0o700))
	old := filepath.Join(fx.backups, ".claude.json.20000101T000000Z")
	must(t, os.WriteFile(old, []byte(httpClaude), 0o600))
	must(t, fx.in.Install(nil))
	out := fx.out.String()
	if !strings.Contains(out, "older backups in "+fx.backups+" still hold header values for pp") || !strings.Contains(out, old) || strings.Contains(out, sentinelValue) {
		t.Fatalf("out %s", out)
	}
}

func TestInstall_NoNoteWithoutOlderHeaderBackups(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	if strings.Contains(fx.out.String(), "older backups") {
		t.Fatalf("out %s", fx.out.String())
	}
}

const escClaude = `{
  "mcpServers": {
    "pp": {"type": "http", "url": "https://pp.example/mcp", "headers": {"Authorization": "Bearer Ax\/y-${AIMUX_TEST_TOK}"}},
    "deepwiki": {"type": "http", "url": "https://mcp.deepwiki.com/mcp"}
  },
  "other": 1
}`

func TestInstallUninstall_HTTPByteIdentical(t *testing.T) {
	t.Setenv("AIMUX_TEST_TOK", sentinelValue)
	fx := newHTTPFX(t, escClaude)
	before, _ := os.ReadFile(fx.claude)
	must(t, fx.in.Install(nil))
	must(t, fx.in.Uninstall())
	after, _ := os.ReadFile(fx.claude)
	if string(before) != string(after) {
		t.Fatalf("not byte identical:\n%s", after)
	}
	if len(fx.vault.items) != 0 {
		t.Fatalf("items left: %d", len(fx.vault.items))
	}
}

func TestUninstall_AfterReformatRestoresOriginalBytes(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	_, membersBefore, _ := readMCPServers(fx.claude)
	must(t, fx.in.Install(nil))
	b, _ := os.ReadFile(fx.claude)
	var v any
	must(t, json.Unmarshal(b, &v))
	pretty, _ := json.MarshalIndent(v, "", "\t") // reordered and respaced
	must(t, os.WriteFile(fx.claude, pretty, 0o600))
	must(t, fx.in.Uninstall())
	_, membersAfter, _ := readMCPServers(fx.claude)
	if string(membersAfter["pp"]) != string(membersBefore["pp"]) {
		t.Fatalf("pp: %s", membersAfter["pp"])
	}
}

func TestUninstall_UnreadableItemChangesNothing(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	before, _ := os.ReadFile(fx.claude)
	manifest, _ := os.ReadFile(fx.manifest)
	fx.vault.getErr = errors.New("locked")
	err := fx.in.Uninstall()
	if err == nil || !strings.Contains(err.Error(), "aimux-mcp-proxy/pp") || strings.Contains(err.Error(), sentinelValue) {
		t.Fatalf("err = %v", err)
	}
	after, _ := os.ReadFile(fx.claude)
	m2, _ := os.ReadFile(fx.manifest)
	if string(before) != string(after) || string(manifest) != string(m2) {
		t.Fatal("something changed")
	}
}

func TestUninstall_DeletesItemsOfRestoredOnly(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	must(t, jsonedit.EditKeyVerbatim(fx.claude, "mcpServers", func(old json.RawMessage) (json.RawMessage, error) {
		return jsonedit.SetMember(old, "pp", json.RawMessage(`{"type":"http","url":"https://changed.example"}`))
	}))
	fx.out.Reset()
	must(t, fx.in.Uninstall())
	if _, ok := fx.vault.items[HeaderService("pp")]; !ok {
		t.Fatal("item of a changed entry deleted")
	}
	if !strings.Contains(fx.out.String(), "Kept Keychain item aimux-mcp-proxy/pp (its entry changed since install)") {
		t.Fatalf("out %s", fx.out.String())
	}
}

func TestForgetRemoved_DeletesItem(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	must(t, jsonedit.EditKeyVerbatim(fx.claude, "mcpServers", func(old json.RawMessage) (json.RawMessage, error) {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(old, &m); err != nil {
			return nil, err
		}
		delete(m, "pp")
		return json.Marshal(m)
	}))
	must(t, fx.in.Install(nil))
	if _, ok := fx.vault.items[HeaderService("pp")]; ok {
		t.Fatal("item of a removed server kept")
	}
}

func TestPlan_HeadersTooLongKeptDirect(t *testing.T) {
	big := strings.Repeat("x", 3000)
	fx := newHTTPFX(t, `{"mcpServers":{"big":{"type":"http","url":"https://big.example/mcp","headers":{"Authorization":"Bearer `+big+`"}}}}`)
	fx.vault.max = 1900
	p, err := fx.in.Plan(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Move) != 0 || !strings.HasPrefix(keptReason(p.KeptDirect, "big"), "header values too long for the Keychain") {
		t.Fatalf("move %v kept %v", p.Move, p.KeptDirect)
	}
}

func TestUninstall_FinishesAfterItemsDeleted(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	before, _ := os.ReadFile(fx.claude)
	must(t, fx.in.Install(nil))
	// An uninstall that restored the config and deleted the items, then
	// stopped before removing the manifest.
	manifest, _ := os.ReadFile(fx.manifest)
	must(t, fx.in.Uninstall())
	must(t, os.WriteFile(fx.manifest, manifest, 0o600)) // #nosec G703 -- the test's temp manifest
	if len(fx.vault.items) != 0 {
		t.Fatal("items left")
	}
	if err := fx.in.Uninstall(); err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
	after, _ := os.ReadFile(fx.claude)
	if string(before) != string(after) {
		t.Fatal("config not restored")
	}
}

func TestInstall_HTTPEntryChangedAfterPlanRefused(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	refresh := fx.in.RefreshHTTP
	fx.in.RefreshHTTP = func(catalogPath string, entries map[string]HTTPEntry, names []string) map[string]error {
		b, _ := os.ReadFile(fx.claude)                                                                                                              // #nosec G304 -- the test's temp config
		must(t, os.WriteFile(fx.claude, []byte(strings.Replace(string(b), "https://mcp.deepwiki.com/mcp", "https://moved.example/mcp", 1)), 0o600)) // #nosec G703 -- the test's temp config
		return refresh(catalogPath, entries, names)
	}
	if err := fx.in.Install(nil); err == nil || !strings.Contains(err.Error(), "deepwiki changed while installing") {
		t.Fatalf("err = %v", err)
	}
}

func TestUninstall_RestoredBackupGetsValuesBack(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	before, _ := os.ReadFile(fx.claude) // #nosec G304 -- the test's temp config
	must(t, fx.in.Install(nil))
	backups := filesUnder(t, fx.backups)
	b, _ := os.ReadFile(backups[0])            // #nosec G304 -- the test's temp backup
	must(t, os.WriteFile(fx.claude, b, 0o600)) // #nosec G703 -- the owner put the backup back
	must(t, fx.in.Uninstall())
	after, _ := os.ReadFile(fx.claude) // #nosec G304 -- the test's temp config
	if string(after) != string(before) {
		t.Fatalf("values not back:\n%s", after)
	}
	if len(fx.vault.items) != 0 {
		t.Fatal("item kept after restoring")
	}
}

func TestInstallStatus_HTTPNotProxiedAndKeptDirect(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install([]string{"pp"}))
	fx.reach = map[string]HTTPReach{} // the down server is up now; status must not know
	fx.in.CheckHTTP = func(context.Context, string, map[string]string) HTTPReach {
		t.Fatal("status probed the network")
		return Reachable
	}
	must(t, jsonedit.EditKeyVerbatim(fx.claude, "mcpServers", func(old json.RawMessage) (json.RawMessage, error) {
		return jsonedit.SetMember(old, "brand", json.RawMessage(`{"type":"http","url":"https://b/mcp","oauth":{}}`))
	}))
	st, err := fx.in.InstallStatus()
	must(t, err)
	if strings.Join(st.NotProxied, ",") != "devsrv" || strings.Join(st.Skipped, ",") != "pp" {
		t.Fatalf("notProxied %v skipped %v", st.NotProxied, st.Skipped)
	}
	if keptReason(st.KeptDirect, "granola") != ReasonNeedsOAuth || keptReason(st.KeptDirect, "brand") != ReasonNeedsOAuth {
		t.Fatalf("kept %v", st.KeptDirect)
	}
	fx.out.Reset()
	fx.in.PrintInstallStatus(st)
	if !strings.Contains(fx.out.String(), "kept direct: needs OAuth (later): brand, granola") {
		t.Fatalf("out %s", fx.out.String())
	}
}

func TestInstallStatus_HTTPDrift(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	must(t, jsonedit.EditKeyVerbatim(fx.claude, "mcpServers", func(old json.RawMessage) (json.RawMessage, error) {
		return jsonedit.SetMember(old, "deepwiki", json.RawMessage(`{"type":"http","url":"https://mcp.deepwiki.com/mcp"}`))
	}))
	st, err := fx.in.InstallStatus()
	must(t, err)
	if !contains(st.Drifted, "deepwiki") || contains(st.NotProxied, "deepwiki") {
		t.Fatalf("drifted %v notProxied %v", st.Drifted, st.NotProxied)
	}
}

func TestUninstall_PartlyEditedBackupKeepsItem(t *testing.T) {
	two := strings.Replace(httpClaude, `"headers": {"Authorization": "Bearer `+sentinelValue+`"}`, `"headers": {"Authorization": "Bearer `+sentinelValue+`", "X-Team": "t-42"}`, 1)
	fx := newHTTPFX(t, two)
	must(t, fx.in.Install(nil))
	b, _ := os.ReadFile(filesUnder(t, fx.backups)[0]) // #nosec G304 -- the test's temp backup
	edited := strings.Replace(string(b), `"X-Team": `+headerPlaceholder, `"X-Team": "mine"`, 1)
	must(t, os.WriteFile(fx.claude, []byte(edited), 0o600)) // #nosec G703 -- the test's temp config
	must(t, fx.in.Uninstall())
	if _, ok := fx.vault.items[HeaderService("pp")]; !ok {
		t.Fatal("item deleted while a placeholder is still in the entry")
	}
}

func TestHookAdded_DownHTTPServerNotAnnouncedAsMoved(t *testing.T) {
	fx := newHTTPFX(t, `{"mcpServers":{"slack":{"command":"slack-mcp","args":[]}}}`)
	must(t, fx.in.Install(nil))
	must(t, jsonedit.EditKeyVerbatim(fx.claude, "mcpServers", func(old json.RawMessage) (json.RawMessage, error) {
		cur, err := jsonedit.SetMember(old, "devsrv", json.RawMessage(`{"type":"http","url":"http://127.0.0.1:3033/mcp"}`))
		if err != nil {
			return nil, err
		}
		cur, err = jsonedit.SetMember(cur, "granola", json.RawMessage(`{"type":"http","url":"https://mcp.granola.ai/mcp"}`))
		if err != nil {
			return nil, err
		}
		return jsonedit.SetMember(cur, "deepwiki", json.RawMessage(`{"type":"http","url":"https://mcp.deepwiki.com/mcp"}`))
	}))
	r := fx.in.HookAdded("claude mcp add --transport http deepwiki https://mcp.deepwiki.com/mcp", "/w")
	if !strings.Contains(r.User, MovedMessage([]string{"deepwiki"})) || strings.Contains(r.User, "devsrv moved") || strings.Contains(r.User, "granola moved") {
		t.Fatalf("reply %q", r.User)
	}
	if !strings.Contains(r.User, "devsrv stayed direct for now") {
		t.Fatalf("reply %q", r.User)
	}
}

func TestInstallStatus_RecordedOAuthNameNowStdio(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	fx.setEntry("granola", json.RawMessage(`{"command": "granola-mcp", "args": []}`))
	st, err := fx.in.InstallStatus()
	must(t, err)
	if !contains(st.NotProxied, "granola") || keptReason(st.KeptDirect, "granola") != "" {
		t.Fatalf("notProxied %v kept %v", st.NotProxied, st.KeptDirect)
	}
}

func TestInstall_RemovedDuringInstallDeletesItsItem(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	fx.in.edit = func(path, key string, fn func(json.RawMessage) (json.RawMessage, error)) error {
		if path == fx.claude {
			must(t, jsonedit.EditKeyVerbatim(path, key, func(old json.RawMessage) (json.RawMessage, error) {
				var m map[string]json.RawMessage
				if err := json.Unmarshal(old, &m); err != nil {
					return nil, err
				}
				delete(m, "pp")
				return json.Marshal(m)
			}))
		}
		return jsonedit.EditKeyVerbatim(path, key, fn)
	}
	must(t, fx.in.Install(nil))
	if _, ok := fx.vault.items[HeaderService("pp")]; ok {
		t.Fatal("item of a server removed during install kept")
	}
}

func TestInstall_IdenticalHTTPEntriesBothRedactedInBackup(t *testing.T) {
	entry := `{"type": "http", "url": "https://twin.example/mcp", "headers": {"Authorization": "Bearer ` + sentinelValue + `"}}`
	fx := newHTTPFX(t, `{"mcpServers": {"a": `+entry+`, "b": `+entry+`}}`)
	must(t, fx.in.Install(nil))
	b, _ := os.ReadFile(filesUnder(t, fx.backups)[0]) // #nosec G304 -- the test's temp backup
	if strings.Contains(string(b), sentinelValue) || strings.Count(string(b), headerPlaceholder) != 2 {
		t.Fatalf("backup %s", b)
	}
}

func TestRedactConfig_DuplicatesRefused(t *testing.T) {
	e := map[string]HTTPEntry{"pp": {Literals: map[string]string{"A": `"x"`}}}
	for _, cfg := range []string{
		`{"mcpServers":{"pp":{"headers":{"A":"x"}}},"mcpServers":{"pp":{"headers":{"A":"y"}}}}`,
		`{"mcpServers":{"pp":{"headers":{"A":"x"}},"pp":{"headers":{"A":"y"}}}}`,
	} {
		if _, err := redactConfig([]byte(cfg), e); err == nil {
			t.Fatalf("accepted %s", cfg)
		}
	}
}

func TestInstall_HeaderedHTTPNeedsCurrentDaemon(t *testing.T) {
	fx := newHTTPFX(t, `{"mcpServers":{"slack":{"command":"slack-mcp","args":[]}}}`)
	must(t, fx.in.Install(nil))
	fx.in.Version, fx.daemonVersion = "1.2.0", "1.1.0"
	fx.setEntry("pp", json.RawMessage(`{"type": "http", "url": "https://pp.example/mcp", "headers": {"Authorization": "Bearer x-1"}}`))
	before, _ := os.ReadFile(fx.claude) // #nosec G304 -- the test's temp config
	err := fx.in.Install(nil)
	if err == nil || !strings.Contains(err.Error(), "aimux mcp proxy restart") {
		t.Fatalf("err = %v", err)
	}
	after, _ := os.ReadFile(fx.claude) // #nosec G304 -- the test's temp config
	if string(before) != string(after) || len(fx.vault.items) != 0 {
		t.Fatal("something changed")
	}
	fx.daemonVersion = "1.2.0"
	must(t, fx.in.Install(nil))
}

func TestInstall_StdioMoveWithSavedHTTPNeedsCurrentDaemon(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	fx.in.Version, fx.daemonVersion = "1.2.0", "1.1.0"
	fx.setEntry("pypi", json.RawMessage(`{"command": "pypi-mcp", "args": []}`))
	if err := fx.in.Install(nil); err == nil || !strings.Contains(err.Error(), "aimux mcp proxy restart") {
		t.Fatalf("err = %v", err)
	}
}

func TestEdit_SavedHTTPNeedsCurrentDaemon(t *testing.T) {
	dir := t.TempDir()
	manifest, servers := dir+"/m.json", dir+"/s.json"
	m := &Manifest{Version: manifestVersion, Port: 7381, Servers: map[string]MovedEntry{"st": {Original: `{}`, Written: `{}`}}}
	must(t, saveManifest(manifest, m))
	must(t, SaveServerFile(servers, map[string]ServerConfig{"st": {Command: "x"}, "dw": {Type: "http", URL: "https://x"}}, []string{"st", "dw"}))
	status := func() (StatusReport, error) { return StatusReport{Running: true, Version: "1.1.0"}, nil }
	err := EditServer(context.Background(), EditOptions{Server: "st", ManifestPath: manifest, ServersPath: servers, Status: status, Version: "1.2.0"})
	if err == nil || !strings.Contains(err.Error(), "aimux mcp proxy restart") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstall_RemovedServerWithOldDaemonChangesNothing(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	fx.in.Version, fx.daemonVersion = "1.2.0", "1.1.0"
	must(t, jsonedit.EditKeyVerbatim(fx.claude, "mcpServers", func(old json.RawMessage) (json.RawMessage, error) {
		return removeMember(old, "slack")
	}))
	before, _ := os.ReadFile(fx.servers) // #nosec G304 -- the test's temp file
	if err := fx.in.Install(nil); err == nil || !strings.Contains(err.Error(), "aimux mcp proxy restart") {
		t.Fatalf("err = %v", err)
	}
	after, _ := os.ReadFile(fx.servers) // #nosec G304 -- the test's temp file
	if string(before) != string(after) {
		t.Fatal("servers file rewritten under an older daemon")
	}
}

func TestCheckDaemonReadsHTTP(t *testing.T) {
	old := func() (StatusReport, error) { return StatusReport{Running: true, Version: "1.1.0"}, nil }
	if err := CheckDaemonReadsHTTP(old, "1.2.0", map[string]ServerConfig{"dw": {Type: "http"}}, ""); err == nil {
		t.Fatal("older daemon with HTTP definitions accepted")
	}
	if err := CheckDaemonReadsHTTP(old, "1.2.0", map[string]ServerConfig{"st": {Command: "x"}}, ""); err != nil {
		t.Fatalf("stdio only refused: %v", err)
	}
}

func TestCheckDaemonReadsHTTP_StatusErrorRefuses(t *testing.T) {
	broken := func() (StatusReport, error) { return StatusReport{}, errors.New("timeout") }
	if err := CheckDaemonReadsHTTP(broken, "1.2.0", map[string]ServerConfig{"dw": {Type: "http"}}, ""); err == nil {
		t.Fatal("unverified daemon accepted")
	}
}

// A server the last install recorded as needing OAuth whose entry later
// gained an "oauth" key (Claude Code adds it after a sign-in) is kept
// direct for the same reason twice, from the manifest and from the
// config: status lists it once, with the reason the config gives now.
func TestInstallStatus_KeptDirectOnceWhenRecordedAndInConfig(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	must(t, fx.in.Install(nil))
	must(t, jsonedit.EditKeyVerbatim(fx.claude, "mcpServers", func(old json.RawMessage) (json.RawMessage, error) {
		return jsonedit.SetMember(old, "granola", json.RawMessage(`{"type":"http","url":"https://mcp.granola.ai/mcp","oauth":{"clientId":"x"}}`))
	}))
	st, err := fx.in.InstallStatus()
	must(t, err)
	n := 0
	for _, k := range st.KeptDirect {
		if k.Server == "granola" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("granola kept direct %d times: %v", n, st.KeptDirect)
	}
	if got := KeptDirectLines(st.KeptDirect); len(got) != 1 || got[0] != "kept direct: needs OAuth (later): granola" {
		t.Fatalf("lines %q", got)
	}
}

// KeptDirectLines names a server once per reason even when given twice.
func TestKeptDirectLines_Dedupes(t *testing.T) {
	got := KeptDirectLines([]KeptDirect{{"brand", ReasonNeedsOAuth}, {"amp", ReasonNeedsOAuth}, {"brand", ReasonNeedsOAuth}})
	if len(got) != 1 || got[0] != "kept direct: needs OAuth (later): amp, brand" {
		t.Fatalf("lines %q", got)
	}
}

// A server whose headers install saw refused (401) is kept direct on
// purpose: status lists it under kept direct with that reason, never as
// not behind the proxy (whose fix is install), and the SessionStart hook
// does not name it. Unreachable stays not behind: running install again is
// the fix once it is up.
func TestInstallStatus_RefusedHeadersIsKeptDirectNotNotProxied(t *testing.T) {
	fx := newHTTPFX(t, httpClaude)
	fx.reach["https://pp.example/mcp"] = NeedsOAuth // pp has headers: a 401 means they were refused
	must(t, fx.in.Install(nil))
	m, _ := LoadManifest(fx.manifest)
	if keptReason(m.Direct, "pp") != reasonRefusedHeaders {
		t.Fatalf("manifest direct %v: pp's reason not recorded", m.Direct)
	}
	fx.in.CheckHTTP = func(context.Context, string, map[string]string) HTTPReach {
		t.Fatal("status probed the network")
		return Reachable
	}
	st, err := fx.in.InstallStatus()
	must(t, err)
	if contains(st.NotProxied, "pp") || !contains(st.NotProxied, "devsrv") {
		t.Fatalf("notProxied %v: want devsrv only, not pp", st.NotProxied)
	}
	if keptReason(st.KeptDirect, "pp") != reasonRefusedHeaders {
		t.Fatalf("kept %v: pp not kept direct with its reason", st.KeptDirect)
	}
	if r := fx.in.HookCheck("/w"); strings.Contains(r.User, "pp") || !strings.Contains(r.User, "devsrv") {
		t.Fatalf("session-start line %q: want devsrv named, pp not", r.User)
	}
}
