// Package aimuxtools holds the aimux-tools runner image's Containerfile and
// the trimmed GitHub provider profile, embedded so aimux sandbox setup can
// build and import them from an installed binary.
package aimuxtools

import _ "embed"

// Containerfile builds profile.RunnerImage.
//
//go:embed Containerfile
var Containerfile []byte

// GitHubProfile is the OpenShell provider profile "aimux-github" that
// provider aimux-github is created with.
//
//go:embed github-profile.yaml
var GitHubProfile []byte
