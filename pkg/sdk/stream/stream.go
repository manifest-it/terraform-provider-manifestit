package stream

import (
	"context"

	"github.com/rs/zerolog"
)

// Params defines parameters for streaming data
type Params[T any] struct {
	Channel   chan []T
	BatchSize int
}

// Do send items to a channel in batches
// T is the type of items being streamed
func Do[T any](ctx context.Context, items []T, channel chan []T, batchSize int, logger zerolog.Logger, itemType string) error {
	if len(items) == 0 {
		return nil
	}

	if batchSize > 0 && len(items) >= batchSize {
		// Send in batches
		for i := 0; i < len(items); i += batchSize {
			end := i + batchSize
			if end > len(items) {
				end = len(items)
			}
			batch := items[i:end]

			select {
			case <-ctx.Done():
				logger.Warn().Str("type", itemType).Msg("context cancelled during streaming")
				return ctx.Err()
			case channel <- batch:
				logger.Debug().
					Str("type", itemType).
					Int("batch_start", i).
					Int("batch_size", len(batch)).
					Msg("streamed batch")
			}
		}
	} else {
		// Send all items as single batch
		select {
		case <-ctx.Done():
			logger.Warn().Str("type", itemType).Msg("context cancelled during streaming")
			return ctx.Err()
		case channel <- items:
			logger.Debug().
				Str("type", itemType).
				Int("item_count", len(items)).
				Msg("streamed batch")
		}
	}

	return nil
}
