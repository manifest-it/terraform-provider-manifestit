package collectors

import (
	"context"
	"fmt"
	"io"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3StateReader reads Terraform state from an S3 bucket.
type S3StateReader struct{}

func (r *S3StateReader) ReadState(ctx context.Context, cfg *BackendConfig) ([]byte, error) {
	if cfg.Bucket == "" || cfg.Key == "" {
		return nil, fmt.Errorf("s3 backend requires bucket and key")
	}

	opts := []func(*awsconfig.LoadOptions) error{}
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg)
	result, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &cfg.Bucket,
		Key:    &cfg.Key,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 GetObject failed: %w", err)
	}
	defer result.Body.Close()

	return io.ReadAll(result.Body)
}
