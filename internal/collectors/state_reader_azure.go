package collectors

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
)

// AzureStateReader reads Terraform state from Azure Blob Storage.
type AzureStateReader struct{}

func (r *AzureStateReader) ReadState(ctx context.Context, cfg *BackendConfig) ([]byte, error) {
	if cfg.StorageAccountName == "" || cfg.ContainerName == "" || cfg.Key == "" {
		return nil, fmt.Errorf("azurerm backend requires storage_account_name, container_name, and key")
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Azure credential: %w", err)
	}

	serviceURL := fmt.Sprintf("https://%s.blob.core.windows.net", cfg.StorageAccountName)
	client, err := azblob.NewClient(serviceURL, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Azure Blob client: %w", err)
	}

	resp, err := client.DownloadStream(ctx, cfg.ContainerName, cfg.Key, nil)
	if err != nil {
		return nil, fmt.Errorf("azure blob download failed: %w", err)
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, resp.Body); err != nil {
		return nil, fmt.Errorf("failed to read azure blob body: %w", err)
	}

	return buf.Bytes(), nil
}
