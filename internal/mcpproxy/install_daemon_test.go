package mcpproxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// installDaemon is a real daemon (Serve with reload, a LiveStarter and a
// CatalogUpdater, loading the servers file and catalog as serve does)
// behind an Installer whose status, probe and reload reach it.
type installDaemon struct {
	updater *CatalogUpdater
	base    string
}

// loadMoved reads what an installed daemon serves: the servers file's
// moved list and the whole catalog.
func loadMoved(servers, catalog string) (ServerSet, error) {
	saved, resolved, err := LoadServerFile(servers)
	if err != nil {
		return ServerSet{}, err
	}
	cfgs := map[string]ServerConfig{}
	for _, r := range resolved {
		if c, ok := saved[r]; ok {
			cfgs[r] = c
		}
	}
	cat, err := LoadCatalog(catalog)
	if err != nil {
		return ServerSet{}, err
	}
	return ServerSet{Configs: cfgs, Resolved: resolved, Catalog: cat}, nil
}

func startInstallDaemon(t *testing.T, fx *installFX) *installDaemon {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	set, err := loadMoved(fx.servers, fx.catalog)
	must(t, err)
	updater := NewCatalogUpdater(set.Catalog, fx.catalog)
	starter := NewLiveStarter(set.Configs, set.Resolved)
	pool := NewPool(starter.Start, PoolOptions{})
	served := set.Catalog.Only(func(s string) bool { _, ok := set.Configs[s]; return ok })
	mon := &Monitor{Pool: pool, Servers: sortedServerNames(set.Configs), Registered: map[string]string{}}
	for name, e := range served.Servers {
		mon.Registered[name] = ToolsFingerprint(e.Tools)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	reload := &ReloadOptions{
		Current: set,
		Load:    func() (ServerSet, error) { return loadMoved(fx.servers, fx.catalog) },
		Apply: func(next ServerSet) {
			starter.Set(next.Configs, next.Resolved)
			updater.Replace(next.Catalog)
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ln, NewServers(served, pool), pool, DaemonOptions{Status: mon, Reload: reload})
	}()
	t.Cleanup(func() { cancel(); <-done })

	fx.lc.loaded = true // the launchd agent runs this daemon
	fx.in.Port = port
	fx.in.Status = func() (StatusReport, error) { return mon.Report(), nil }
	fx.in.Probe = func(names []string) (StatusReport, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return mon.ProbeServers(ctx, names), nil
	}
	fx.in.Reload = func() (ReloadResult, error) { return ReloadDaemon(port) }
	return &installDaemon{updater: updater, base: "http://" + ln.Addr().String()}
}

// A backend starting in the daemon between install's catalog refresh and
// its /reload saves the daemon's catalog: install's new entry must survive
// that, get its endpoint on reload, and only then be written to Claude's
// config.
func TestInstall_DaemonCatalogRefreshBeforeReload_NewServerStillServed(t *testing.T) {
	testServer := fmt.Sprintf(`{"command": %q, "env": {%q: "1"}}`, os.Args[0], testServerEnv)
	cfg := fmt.Sprintf(`{"mcpServers": {"old": {"type": "http", "url": "set below"}, "new": %s}}`, testServer)
	fx := newInstallFX(t, cfg, "")
	must(t, SaveServerFile(fx.servers, map[string]ServerConfig{"old": testConfigs()["test"]}, []string{"old"}))
	cat := &Catalog{}
	cat.Set("old", toolJSON("echo"), time.Now())
	must(t, cat.Save(fx.catalog))
	d := startInstallDaemon(t, fx)
	fx.setEntry("old", fx.in.entryFor("old"))
	must(t, saveManifest(fx.manifest, &Manifest{Version: manifestVersion, Port: fx.in.Port, Servers: map[string]MovedEntry{
		"old": {Original: testServer, Written: string(fx.in.entryFor("old"))},
	}}))

	cliRefresh := fx.in.Refresh
	fx.in.Refresh = func(path string, names []string) map[string]error {
		errs := cliRefresh(path, names) // install lists "new" into the catalog file
		// ... and a session's first call starts "old" in the daemon, which
		// saves the daemon's catalog before install reloads it.
		must(t, d.updater.Refresh(context.Background(), "old", &listingBackend{tools: toolJSON("echo")}))
		return errs
	}
	must(t, fx.in.Install(nil))

	resp, err := http.Post(d.base+EndpointPath("new"), "application/json", nil)
	must(t, err)
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Fatal("Claude's config points at /mcp/new but the daemon serves no such endpoint")
	}
	if got := entryURL(fx.entries()["new"]); got != EntryURL(fx.in.Port, "new") {
		t.Fatalf("new = %q", got)
	}
	saved, err := LoadCatalog(fx.catalog)
	must(t, err)
	if _, ok := saved.Servers["new"]; !ok {
		t.Fatal("the daemon's catalog save erased install's entry")
	}
}
