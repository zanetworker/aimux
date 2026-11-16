package remoteapi

import (
	"net/http"
	"sync"
)

// testClients holds each test API's client function (setClientOf).
var testClients sync.Map // *API -> func(*http.Request) string

func init() {
	testClientOf = func(a *API, r *http.Request) string {
		if f, ok := testClients.Load(a); ok {
			return f.(func(*http.Request) string)(r)
		}
		return ""
	}
}

// setClientOf makes f name the client of a's requests, as a local socket
// would; tests only.
func setClientOf(a *API, f func(*http.Request) string) { testClients.Store(a, f) }
