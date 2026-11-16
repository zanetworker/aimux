package aimuxtools

import (
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/profile"
	"gopkg.in/yaml.v3"
)

// The profile opens api.github.com to gh only: no github.com git transport
// and no git binaries, as the stock profile has. Its id is not the stock
// "github", so a built-in profile can never stand in for it.
func TestGitHubProfile_Trimmed(t *testing.T) {
	var p struct {
		ID        string `yaml:"id"`
		Endpoints []struct {
			Host   string `yaml:"host"`
			Access string `yaml:"access"`
			Rules  []any  `yaml:"rules"`
		} `yaml:"endpoints"`
		Binaries []string `yaml:"binaries"`
	}
	if err := yaml.Unmarshal(GitHubProfile, &p); err != nil {
		t.Fatal(err)
	}
	if p.ID != "aimux-github" || len(p.Endpoints) != 2 || strings.Join(p.Binaries, ",") != "/usr/bin/gh" {
		t.Fatalf("profile = %+v", p)
	}
	for _, e := range p.Endpoints {
		if e.Host != "api.github.com" || e.Access != "read-only" || len(e.Rules) != 0 {
			t.Fatalf("endpoint = %+v", e)
		}
	}
}

// The Containerfile builds on the sandbox image and installs the gh version
// RunnerImage is tagged with, checked against both architectures' sums.
func TestContainerfile_Pins(t *testing.T) {
	cf := string(Containerfile)
	_, tag, _ := strings.Cut(profile.RunnerImage, ":")
	for _, want := range []string{
		"FROM " + profile.SandboxImage + "\n",
		"ARG GH_VERSION=" + tag + "\n",
		"ee32df2323c8847f5fac6cb6b4b69ca75828e9329a8f22f677021f7b3306ce63",
		"4c806978ffce1cec70853302a56d70ac6da4f19bae7adca4e1a8ab00633dd91d",
		"sha256sum -c -",
		"/usr/bin/gh",
	} {
		if !strings.Contains(cf, want) {
			t.Errorf("Containerfile lacks %q", want)
		}
	}
}
