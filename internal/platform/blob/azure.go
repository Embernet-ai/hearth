package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
)

// azureBackend is Azure Blob Storage through the official Azure SDK for Go.
// Authentication is the storage account's shared key, which also signs the SAS
// URLs that stand in for presigned ones.
type azureBackend struct {
	client    *azblob.Client
	cred      *azblob.SharedKeyCredential
	container string
	base      string
}

func openAzure(ctx context.Context, cfg Config) (*azureBackend, error) {
	if cfg.AzureAccount == "" {
		return nil, errors.New("blob: AzureAccount is required")
	}
	if cfg.AzureKey == "" {
		return nil, errors.New("blob: AzureKey is required")
	}
	base := strings.TrimRight(cfg.AzureEndpoint, "/")
	if base == "" {
		base = fmt.Sprintf("https://%s.blob.core.windows.net", cfg.AzureAccount)
	}
	cred, err := azblob.NewSharedKeyCredential(cfg.AzureAccount, cfg.AzureKey)
	if err != nil {
		return nil, fmt.Errorf("blob: azure credential: %w", err)
	}
	client, err := azblob.NewClientWithSharedKeyCredential(base, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("blob: azure client: %w", err)
	}
	a := &azureBackend{client: client, cred: cred, container: cfg.Bucket, base: base}
	if _, err := client.CreateContainer(ctx, cfg.Bucket, nil); err != nil &&
		!bloberror.HasCode(err, bloberror.ContainerAlreadyExists) {
		return nil, fmt.Errorf("blob: reach azure storage at %s: %w", base, err)
	}
	return a, nil
}

func (a *azureBackend) Name() string     { return BackendAzure }
func (a *azureBackend) Bucket() string   { return a.container }
func (a *azureBackend) Endpoint() string { return a.base }

func (a *azureBackend) blobClient(key string) *blob.Client {
	return a.client.ServiceClient().NewContainerClient(a.container).NewBlobClient(key)
}

func (a *azureBackend) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (int64, error) {
	cr := &countingReader{r: r}
	opts := &azblob.UploadStreamOptions{}
	if contentType != "" {
		opts.HTTPHeaders = &blob.HTTPHeaders{BlobContentType: &contentType}
	}
	if _, err := a.client.UploadStream(ctx, a.container, key, cr, opts); err != nil {
		return 0, err
	}
	return cr.n, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (a *azureBackend) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	resp, err := a.client.DownloadStream(ctx, a.container, key, nil)
	if err != nil {
		return nil, azureErr(err)
	}
	return resp.Body, nil
}

func (a *azureBackend) Stat(ctx context.Context, key string) (Object, error) {
	p, err := a.blobClient(key).GetProperties(ctx, nil)
	if err != nil {
		return Object{}, azureErr(err)
	}
	o := Object{Key: key}
	if p.ContentLength != nil {
		o.Size = *p.ContentLength
	}
	if p.ETag != nil {
		o.ETag = strings.Trim(string(*p.ETag), `"`)
	}
	if p.ContentType != nil {
		o.ContentType = *p.ContentType
	}
	return o, nil
}

func azureErr(err error) error {
	if bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ResourceNotFound) {
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	return err
}

func (a *azureBackend) Remove(ctx context.Context, key string) error {
	_, err := a.client.DeleteBlob(ctx, a.container, key, nil)
	if err != nil && !bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ResourceNotFound) {
		return err
	}
	return nil
}

// sas makes a read or write SAS URL for one blob, signed with the account key.
func (a *azureBackend) sasURL(key string, perms sas.BlobPermissions, ttl time.Duration, disposition string) (*url.URL, error) {
	vals := sas.BlobSignatureValues{
		Protocol:           sas.ProtocolHTTPSandHTTP,
		StartTime:          time.Now().UTC().Add(-5 * time.Minute),
		ExpiryTime:         time.Now().UTC().Add(ttl),
		Permissions:        perms.String(),
		ContainerName:      a.container,
		BlobName:           key,
		ContentDisposition: disposition,
	}
	q, err := vals.SignWithSharedKey(a.cred)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(a.blobClient(key).URL())
	if err != nil {
		return nil, err
	}
	u.RawQuery = q.Encode()
	return u, nil
}

func (a *azureBackend) PresignGet(ctx context.Context, key, downloadName string, ttl time.Duration) (*url.URL, error) {
	disp := ""
	if downloadName != "" {
		disp = fmt.Sprintf(`attachment; filename="%s"`, sanitiseFilename(downloadName))
	}
	return a.sasURL(key, sas.BlobPermissions{Read: true}, ttl, disp)
}

func (a *azureBackend) PresignPut(ctx context.Context, key string, ttl time.Duration) (*url.URL, error) {
	return a.sasURL(key, sas.BlobPermissions{Create: true, Write: true}, ttl, "")
}

func (a *azureBackend) Probe(ctx context.Context) error {
	_, err := a.client.ServiceClient().NewContainerClient(a.container).GetProperties(ctx, nil)
	return err
}

func (a *azureBackend) List(ctx context.Context, fn func(Object) error) error {
	pager := a.client.NewListBlobsFlatPager(a.container, &container.ListBlobsFlatOptions{})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, it := range page.Segment.BlobItems {
			if it == nil || it.Name == nil {
				continue
			}
			o := Object{Key: *it.Name}
			if it.Properties != nil {
				if it.Properties.ContentLength != nil {
					o.Size = *it.Properties.ContentLength
				}
				if it.Properties.ETag != nil {
					o.ETag = strings.Trim(string(*it.Properties.ETag), `"`)
				}
			}
			if err := fn(o); err != nil {
				return err
			}
		}
	}
	return nil
}
