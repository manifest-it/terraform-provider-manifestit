package collectors

import (
	"context"
	"sync"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// CollectCloudIdentities detects and collects identity for all available cloud providers in parallel.
// Each cloud collector runs with an individual 3-second timeout. If one fails, others still succeed.
func (c *Collector) CollectCloudIdentities(ctx context.Context) []CloudIdentity {
	type result struct {
		identity *CloudIdentity
	}

	var (
		mu      sync.Mutex
		results []CloudIdentity
		wg      sync.WaitGroup
	)

	collectors := []struct {
		name    string
		collect func(ctx context.Context) *CloudIdentity
	}{
		{"aws", c.collectAWS},
		{"azure", c.collectAzure},
		{"gcp", c.collectGCP},
	}

	for _, col := range collectors {
		wg.Add(1)
		go func(name string, fn func(ctx context.Context) *CloudIdentity) {
			defer wg.Done()

			cloudCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()

			id := fn(cloudCtx)
			if id == nil {
				return
			}

			tflog.Debug(ctx, "collected cloud identity", map[string]interface{}{
				"provider": name,
			})

			mu.Lock()
			results = append(results, *id)
			mu.Unlock()
		}(col.name, col.collect)
	}

	wg.Wait()
	return results
}
