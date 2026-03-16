package collectors

import (
	"context"
	"fmt"
)

// MultiStateReader dispatches to the appropriate backend-specific reader
// based on the BackendConfig.Type field.
type MultiStateReader struct {
	readers map[string]StateReader
}

// NewMultiStateReader creates a StateReader that supports S3, Azure, and GCS backends.
func NewMultiStateReader() *MultiStateReader {
	return &MultiStateReader{
		readers: map[string]StateReader{
			"s3":      &S3StateReader{},
			"azurerm": &AzureStateReader{},
			"gcs":     &GCSStateReader{},
		},
	}
}

func (m *MultiStateReader) ReadState(ctx context.Context, cfg *BackendConfig) ([]byte, error) {
	reader, ok := m.readers[cfg.Type]
	if !ok {
		return nil, fmt.Errorf("unsupported backend type: %s", cfg.Type)
	}
	return reader.ReadState(ctx, cfg)
}
