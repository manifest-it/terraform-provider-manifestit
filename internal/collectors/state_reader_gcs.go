package collectors

import (
	"context"
	"fmt"
	"io"
	"path"

	"cloud.google.com/go/storage"
)

// GCSStateReader reads Terraform state from a Google Cloud Storage bucket.
type GCSStateReader struct{}

func (r *GCSStateReader) ReadState(ctx context.Context, cfg *BackendConfig) ([]byte, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("gcs backend requires bucket")
	}

	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCS client: %w", err)
	}
	defer client.Close()

	// Terraform GCS backend stores state at <prefix>/default.tfstate
	objectName := "default.tfstate"
	if cfg.Prefix != "" {
		objectName = path.Join(cfg.Prefix, objectName)
	}

	reader, err := client.Bucket(cfg.Bucket).Object(objectName).NewReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("gcs read failed: %w", err)
	}
	defer reader.Close()

	return io.ReadAll(reader)
}
