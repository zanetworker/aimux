package profile

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func parentChild(parent, child Manifest) map[string]Manifest {
	parent.Name = "p"
	child.Name = "c"
	child.Extends = "p"
	return map[string]Manifest{"p": parent, "c": child}
}

func TestResolve_ChildNarrowsTools(t *testing.T) {
	user := parentChild(
		Manifest{Tools: []string{"a", "b"}},
		Manifest{Tools: []string{"a"}},
	)
	got, err := Resolve("c", user)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Tools, []string{"a"}) {
		t.Fatalf("tools = %v, want [a]", got.Tools)
	}
}

func TestResolve_ChildOmittingFieldsInheritsParent(t *testing.T) {
	user := parentChild(
		Manifest{Tools: []string{"a", "b"}, Network: NetworkSpec{Default: "deny", Allow: []string{"model"}}, Runs: "sandbox"},
		Manifest{},
	)
	got, err := Resolve("c", user)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Tools, []string{"a", "b"}) || got.Network.Default != "deny" || got.Runs != "sandbox" {
		t.Fatalf("not inherited: %+v", got)
	}
}

func TestResolve_ChildWideningToolsIsErrWidens(t *testing.T) {
	user := parentChild(
		Manifest{Tools: []string{"a"}},
		Manifest{Tools: []string{"a", "extra-tool"}},
	)
	_, err := Resolve("c", user)
	if !errors.Is(err, ErrWidens) {
		t.Fatalf("err = %v, want ErrWidens", err)
	}
	if !strings.Contains(err.Error(), "tools") || !strings.Contains(err.Error(), "extra-tool") {
		t.Fatalf("message %q must name field and added value", err)
	}
}

func TestResolve_ChildWideningNetworkIsErrWidens(t *testing.T) {
	cases := map[string]struct {
		parent, child NetworkSpec
		wantErr       bool
		wantInMsg     string
	}{
		"adds allow":         {NetworkSpec{"deny", []string{"model"}}, NetworkSpec{"deny", []string{"model", "web"}}, true, "web"},
		"deny back to allow": {NetworkSpec{"deny", nil}, NetworkSpec{"allow", nil}, true, "network.default"},
		"allow to deny":      {NetworkSpec{"allow", nil}, NetworkSpec{"deny", nil}, false, ""},
		"narrows allow":      {NetworkSpec{"deny", []string{"model", "web"}}, NetworkSpec{"deny", []string{"model"}}, false, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Resolve("c", parentChild(Manifest{Network: c.parent}, Manifest{Network: c.child}))
			if c.wantErr {
				if !errors.Is(err, ErrWidens) || !strings.Contains(err.Error(), c.wantInMsg) {
					t.Fatalf("err = %v, want ErrWidens naming %q", err, c.wantInMsg)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResolve_ChildWideningFilesCredentialsSettingsIsErrWidens(t *testing.T) {
	cases := map[string]struct {
		parent, child Manifest
		want          string
	}{
		"files read":   {Manifest{Files: FilesSpec{Read: []string{"/a"}}}, Manifest{Files: FilesSpec{Read: []string{"/a", "/b"}}}, "files.read"},
		"files write":  {Manifest{Files: FilesSpec{Write: []string{"/a"}}}, Manifest{Files: FilesSpec{Write: []string{"/b"}}}, "files.write"},
		"credentials":  {Manifest{Credentials: []string{"x"}}, Manifest{Credentials: []string{"x", "y"}}, "credentials"},
		"inherit_user": {Manifest{}, Manifest{Settings: SettingsSpec{InheritUser: true}}, "inherit_user"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Resolve("c", parentChild(c.parent, c.child))
			if !errors.Is(err, ErrWidens) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want ErrWidens naming %q", err, c.want)
			}
		})
	}
}

func TestResolve_InheritUserMayGoTrueToFalse(t *testing.T) {
	user := parentChild(
		Manifest{Settings: SettingsSpec{InheritUser: true}},
		Manifest{Settings: SettingsSpec{InheritUser: false}},
	)
	got, err := Resolve("c", user)
	if err != nil {
		t.Fatal(err)
	}
	if got.Settings.InheritUser {
		t.Fatal("inherit_user should be false")
	}
}

func TestResolve_Cycle(t *testing.T) {
	user := map[string]Manifest{
		"a": {Name: "a", Extends: "b"},
		"b": {Name: "b", Extends: "a"},
	}
	_, err := Resolve("a", user)
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("err = %v, want ErrCycle", err)
	}
}

func TestResolve_UnknownParent(t *testing.T) {
	user := map[string]Manifest{"a": {Name: "a", Extends: "nope"}}
	_, err := Resolve("a", user)
	if !errors.Is(err, ErrUnknownProfile) || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v, want ErrUnknownProfile naming nope", err)
	}
	if _, err := Resolve("missing", nil); !errors.Is(err, ErrUnknownProfile) {
		t.Fatalf("err = %v, want ErrUnknownProfile", err)
	}
}

func TestResolve_UserCannotShadowBuiltin(t *testing.T) {
	_, err := Resolve("base", map[string]Manifest{"base": {Name: "base"}})
	if err == nil || !strings.Contains(err.Error(), "base.yaml") {
		t.Fatalf("err = %v, want error naming base.yaml", err)
	}
}

func TestLoadDir_UnknownFieldRejected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "bad.yaml", "name: bad\ntool: [a]\n")
	_, err := LoadDir(dir)
	if err == nil || !strings.Contains(err.Error(), "bad.yaml") || !strings.Contains(err.Error(), "tool") {
		t.Fatalf("err = %v, want error naming file and field", err)
	}
}

func TestLoadDir_NameMustMatchFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "one.yaml", "name: two\n")
	_, err := LoadDir(dir)
	if err == nil || !strings.Contains(err.Error(), "one.yaml") {
		t.Fatalf("err = %v, want error naming one.yaml", err)
	}
}

func TestLoadDir_LoadsAndIgnoresOtherFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "mine.yaml", "name: mine\nextends: builtin:base\nruns: acp\ntools: []\n")
	writeFile(t, dir, "notes.txt", "ignore me")
	got, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := got["mine"]
	if len(got) != 1 || !ok || m.Runs != "acp" || m.Extends != "builtin:base" || m.Tools == nil {
		t.Fatalf("got %+v", got)
	}
	empty, err := LoadDir(filepath.Join(dir, "absent"))
	if err != nil || len(empty) != 0 {
		t.Fatalf("missing dir: %v %v", empty, err)
	}
}

func TestLoadDir_ShadowingBuiltinRejected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "page.yaml", "name: page\nextends: builtin:base\n")
	_, err := LoadDir(dir)
	if err == nil || !strings.Contains(err.Error(), "page.yaml") {
		t.Fatalf("err = %v, want error naming page.yaml", err)
	}
}

func TestResolve_WildcardRejectedUnlessUnsafe(t *testing.T) {
	_, err := Resolve("w", map[string]Manifest{"w": {Name: "w", Tools: []string{"jira:*"}}})
	if !errors.Is(err, ErrWildcard) {
		t.Fatalf("err = %v, want ErrWildcard", err)
	}
	got, err := Resolve("w", map[string]Manifest{"w": {Name: "w", Tools: []string{"jira:*"}, UnsafePatterns: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Tools, []string{"jira:*"}) {
		t.Fatalf("tools = %v", got.Tools)
	}
}

func loadOne(t *testing.T, body string) Manifest {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "x.yaml", body)
	got, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Resolve("x", got)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDigest_StableAcrossKeyOrder(t *testing.T) {
	a := loadOne(t, "name: x\nextends: builtin:work-readonly\ntools: [jira:readonly, wiki:readonly]\nnetwork:\n  default: deny\n  allow: [model, gateway]\n")
	b := loadOne(t, "network:\n  allow: [gateway, model]\n  default: deny\ntools: [wiki:readonly, jira:readonly]\nextends: builtin:work-readonly\nname: x\n")
	if a.Digest() != b.Digest() {
		t.Fatalf("digests differ: %s vs %s", a.Digest(), b.Digest())
	}
	if !strings.HasPrefix(a.Digest(), "sha256:") || len(a.Digest()) != len("sha256:")+64 {
		t.Fatalf("bad digest format %q", a.Digest())
	}
}

func TestDigest_ChangesWithTools(t *testing.T) {
	a := loadOne(t, "name: x\nextends: builtin:work-readonly\ntools: [wiki:readonly]\n")
	b := loadOne(t, "name: x\nextends: builtin:work-readonly\ntools: [wiki:readonly, jira:readonly]\n")
	if a.Digest() == b.Digest() {
		t.Fatal("digest must change when tools change")
	}
}

func TestBuiltins_AllResolveAndPageHasNoTools(t *testing.T) {
	for _, name := range []string{"base", "page", "research", "work-readonly", "research-with-work", "coding"} {
		m, err := Resolve(name, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if m.Name != name || m.Harness != "claude-code" {
			t.Errorf("%s: name=%q harness=%q", name, m.Name, m.Harness)
		}
	}
	page, _ := Resolve("page", nil)
	if len(page.Tools) != 0 || page.Runs != "acp" || !reflect.DeepEqual(page.Network.Allow, []string{"model"}) {
		t.Fatalf("page = %+v", page)
	}
	wr, _ := Resolve("work-readonly", nil)
	if wr.Runs != "sandbox" || len(wr.Tools) != 4 || !reflect.DeepEqual(wr.Network.Allow, []string{"model", "gateway"}) {
		t.Fatalf("work-readonly = %+v", wr)
	}
	coding, _ := Resolve("builtin:coding", nil)
	if coding.Runs != "host" || !coding.Settings.InheritUser || coding.Network.Default != "allow" {
		t.Fatalf("coding = %+v", coding)
	}
	base, _ := Resolve("base", nil)
	if base.Runs != "host" || base.Network.Default != "deny" || base.Settings.InheritUser || len(base.Tools) != 0 {
		t.Fatalf("base = %+v", base)
	}
	for name, want := range map[string]bool{"base": true, "page": true, "work-readonly": true, "coding": true, "research": false, "research-with-work": false} {
		m, _ := Resolve(name, nil)
		if m.Available != want {
			t.Errorf("%s Available = %v, want %v", name, m.Available, want)
		}
	}
	rww, _ := Resolve("research-with-work", nil)
	if rww.Runs != "run" || len(rww.Roles) == 0 {
		t.Fatalf("research-with-work = %+v", rww)
	}
}

func TestResolve_UserExtendsBuiltin(t *testing.T) {
	user := map[string]Manifest{"mine": {Name: "mine", Extends: "builtin:work-readonly", Tools: []string{"wiki:readonly"}}}
	got, err := Resolve("mine", user)
	if err != nil {
		t.Fatal(err)
	}
	if got.Runs != "sandbox" || !reflect.DeepEqual(got.Tools, []string{"wiki:readonly"}) || !got.Available {
		t.Fatalf("got %+v", got)
	}
}

func TestMigrateV1_CarriesProviderModelDir(t *testing.T) {
	out, _ := MigrateV1([]Profile{
		{Name: "dev", Provider: "anthropic", Model: "opus", Dir: "/work", Mode: "plan"},
		{Name: "bare"},
	})
	if len(out) != 2 {
		t.Fatalf("len = %d", len(out))
	}
	m := out[0]
	if m.Name != "dev" || m.Extends != "builtin:base" || m.Runs != "host" ||
		m.Model.Provider != "anthropic" || m.Model.Name != "opus" || m.Dir != "/work" || len(m.Tools) != 0 {
		t.Fatalf("m = %+v", m)
	}
	user := map[string]Manifest{"dev": out[0], "bare": out[1]}
	for n := range user {
		if _, err := Resolve(n, user); err != nil {
			t.Fatalf("migrated %s must resolve: %v", n, err)
		}
	}
	if none, renames := MigrateV1(nil); len(none) != 0 || len(renames) != 0 {
		t.Fatal("nil in, empty out")
	}
}

func TestResolve_HarnessDefaultsToClaudeCode(t *testing.T) {
	got, err := Resolve("h", map[string]Manifest{"h": {Name: "h"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Harness != "claude-code" {
		t.Fatalf("harness = %q", got.Harness)
	}
}

func TestResolve_ChildCannotChangeHarness(t *testing.T) {
	user := parentChild(Manifest{Harness: "claude-code"}, Manifest{Harness: "codex"})
	_, err := Resolve("c", user)
	if !errors.Is(err, ErrWidens) || !strings.Contains(err.Error(), "harness") {
		t.Fatalf("err = %v, want ErrWidens naming harness", err)
	}
	user = parentChild(Manifest{Harness: "codex"}, Manifest{})
	got, err := Resolve("c", user)
	if err != nil || got.Harness != "codex" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestResolve_InvalidEnumsRejected(t *testing.T) {
	for name, m := range map[string]Manifest{
		"runs":    {Name: "x", Runs: "cloud"},
		"default": {Name: "x", Network: NetworkSpec{Default: "maybe"}},
		"allow":   {Name: "x", Network: NetworkSpec{Allow: []string{"everything"}}},
	} {
		if _, err := Resolve("x", map[string]Manifest{"x": m}); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestResolve_ChildCannotEscapeSandbox(t *testing.T) {
	user := map[string]Manifest{"c": {Name: "c", Extends: "builtin:work-readonly", Runs: "host"}}
	_, err := Resolve("c", user)
	if !errors.Is(err, ErrWidens) || !strings.Contains(err.Error(), "runs") {
		t.Fatalf("err = %v, want ErrWidens naming runs", err)
	}
}

func TestResolve_RunsTransitions(t *testing.T) {
	cases := []struct {
		parent, child string
		ok            bool
	}{
		{"host", "host", true}, {"host", "acp", true}, {"host", "sandbox", true}, {"acp", "sandbox", true},
		{"acp", "host", false}, {"sandbox", "acp", false}, {"sandbox", "host", false},
		{"run", "run", true}, {"run", "host", false}, {"run", "sandbox", false},
		{"host", "run", false}, {"sandbox", "run", false},
	}
	for _, c := range cases {
		_, err := Resolve("c", parentChild(Manifest{Runs: c.parent}, Manifest{Runs: c.child}))
		if c.ok && err != nil {
			t.Errorf("%s->%s: %v", c.parent, c.child, err)
		}
		if !c.ok && !errors.Is(err, ErrWidens) {
			t.Errorf("%s->%s: err = %v, want ErrWidens", c.parent, c.child, err)
		}
	}
}

func TestMigrateV1_RenamesBuiltinCollision(t *testing.T) {
	out, renames := MigrateV1([]Profile{{Name: "page"}, {Name: "page-v1"}, {Name: "coding", Model: "m"}, {Name: "ok"}})
	names := []string{}
	for _, m := range out {
		names = append(names, m.Name)
	}
	want := []string{"page-v1-2", "page-v1", "coding-v1", "ok"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	if renames["page"] != "page-v1-2" || renames["coding"] != "coding-v1" || len(renames) != 2 {
		t.Fatalf("renames = %v", renames)
	}
	if out[2].Model.Name != "m" {
		t.Fatal("renamed profile must keep its data")
	}
}

func TestResolve_OneShadowingFileDoesNotBreakOthers(t *testing.T) {
	user := map[string]Manifest{
		"base": {Name: "base", Extends: "builtin:base"},
		"ok":   {Name: "ok", Extends: "builtin:base"},
	}
	if _, err := Resolve("ok", user); err != nil {
		t.Fatalf("ok must resolve: %v", err)
	}
	if _, err := Resolve("builtin:base", user); err != nil {
		t.Fatalf("builtin:base must resolve: %v", err)
	}
	if _, err := Resolve("base", user); err == nil || !strings.Contains(err.Error(), "base.yaml") {
		t.Fatalf("err = %v, want error naming base.yaml", err)
	}
}

func TestLoadDir_ShadowingFileDoesNotDropOthers(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "page.yaml", "name: page\nextends: builtin:base\n")
	writeFile(t, dir, "fine.yaml", "name: fine\nextends: builtin:base\n")
	got, err := LoadDir(dir)
	if err == nil || !strings.Contains(err.Error(), "page.yaml") {
		t.Fatalf("err = %v, want error naming page.yaml", err)
	}
	if _, ok := got["fine"]; !ok || len(got) != 1 {
		t.Fatalf("got %v, want only fine", got)
	}
}

func TestLoadDir_UserProfileWithoutExtendsRejected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "root.yaml", "name: root\ntools: [anything]\n")
	_, err := LoadDir(dir)
	if err == nil || !strings.Contains(err.Error(), "root.yaml") || !strings.Contains(err.Error(), "extends") {
		t.Fatalf("err = %v, want error naming root.yaml and extends", err)
	}
}
