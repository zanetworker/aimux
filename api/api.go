// Package api holds aimux's published API contracts, embedded so the server
// can serve them and tests can check the server against them.
package api

import _ "embed"

// RemoteV1 is the OpenAPI document of the remote API (`aimux remote serve`),
// served at /v1/openapi.yaml.
//
//go:embed remote-v1.openapi.yaml
var RemoteV1 []byte
