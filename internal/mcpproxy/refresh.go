package mcpproxy

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

const refreshParallelism = 4

// RefreshCatalog starts each named server, records its tools in cat, and
// stops it again. A server that fails keeps its previous entry; the failures
// are returned by server name.
func RefreshCatalog(ctx context.Context, cat *Catalog, start Starter, servers []string) map[string]error {
	type result struct {
		server string
		tools  []json.RawMessage
		err    error
	}
	results := make(chan result, len(servers))
	sem := make(chan struct{}, refreshParallelism)
	var wg sync.WaitGroup
	for _, s := range servers {
		wg.Add(1)
		go func(server string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			b, err := start(ctx, server)
			if err != nil {
				results <- result{server: server, err: err}
				return
			}
			defer func() { _ = b.Close() }()
			tools, err := b.ListToolsRaw(ctx)
			results <- result{server, tools, err}
		}(s)
	}
	wg.Wait()
	close(results)

	errs := map[string]error{}
	now := time.Now()
	for r := range results {
		if r.err != nil {
			errs[r.server] = r.err
			continue
		}
		cat.Set(r.server, r.tools, now)
	}
	return errs
}
