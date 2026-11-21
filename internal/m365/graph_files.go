package m365

// OneDrive and SharePoint through Microsoft Graph, read and write, as the person.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/microsoftgraph/msgraph-sdk-go/drives"
	"github.com/microsoftgraph/msgraph-sdk-go/models"

	"github.com/Embernet-ai/hearth/internal/connect"
)

// smallUpload is the largest file Graph takes in a single request. A bigger one goes through an upload session.
const smallUpload = 4 << 20

// chunk is the upload session's piece size, which has to be a multiple of 320 KiB.
const chunk = 16 * 320 << 10

func itemFrom(driveID string, it models.DriveItemable) connect.FileItem {
	f := connect.FileItem{ID: s(it.GetId()), DriveID: driveID, Name: s(it.GetName()), WebURL: s(it.GetWebUrl()),
		Folder: it.GetFolder() != nil}
	if it.GetSize() != nil {
		f.Size = *it.GetSize()
	}
	if it.GetFile() != nil {
		f.MIME = s(it.GetFile().GetMimeType())
	}
	if t := it.GetLastModifiedDateTime(); t != nil {
		f.Modified = t.UTC()
	}
	return f
}

// Drives lists the person's OneDrive and the document libraries of the SharePoint sites they follow.
func (x filesAdapter) Drives(ctx context.Context) ([]connect.Drive, error) {
	var out []connect.Drive
	own, err := x.client.Me().Drive().Get(ctx, nil)
	if err != nil {
		return nil, x.fail(ctx, err)
	}
	out = append(out, connect.Drive{ID: s(own.GetId()), Name: "OneDrive", Kind: "personal"})

	sites, err := x.client.Me().FollowedSites().Get(ctx, nil)
	if err != nil {
		// Followed sites need Sites.Read.All, which a tenant may not have approved. OneDrive still works.
		return out, nil
	}
	for _, st := range sites.GetValue() {
		ds, err := x.client.Sites().BySiteId(s(st.GetId())).Drives().Get(ctx, nil)
		if err != nil {
			continue
		}
		for _, d := range ds.GetValue() {
			out = append(out, connect.Drive{ID: s(d.GetId()), Name: s(st.GetDisplayName()) + " / " + s(d.GetName()), Kind: "site"})
		}
	}
	return out, nil
}

func (x filesAdapter) rootID(ctx context.Context, driveID string) (string, error) {
	root, err := x.client.Drives().ByDriveId(driveID).Root().Get(ctx, nil)
	if err != nil {
		return "", x.fail(ctx, err)
	}
	return s(root.GetId()), nil
}

func (x filesAdapter) List(ctx context.Context, driveID, folderID string) ([]connect.FileItem, error) {
	if folderID == "" {
		id, err := x.rootID(ctx, driveID)
		if err != nil {
			return nil, err
		}
		folderID = id
	}
	cfg := &drives.ItemItemsItemChildrenRequestBuilderGetRequestConfiguration{
		QueryParameters: &drives.ItemItemsItemChildrenRequestBuilderGetQueryParameters{Top: ptr(int32(200))},
	}
	b := x.client.Drives().ByDriveId(driveID).Items().ByDriveItemId(folderID).Children()
	var out []connect.FileItem
	resp, err := b.Get(ctx, cfg)
	for {
		if err != nil {
			return nil, x.fail(ctx, err)
		}
		for _, it := range resp.GetValue() {
			out = append(out, itemFrom(driveID, it))
		}
		next := resp.GetOdataNextLink()
		if next == nil || *next == "" || len(out) >= 1000 {
			return out, nil
		}
		resp, err = b.WithUrl(*next).Get(ctx, cfg)
	}
}

func (x filesAdapter) Item(ctx context.Context, driveID, itemID string) (connect.FileItem, error) {
	it, err := x.client.Drives().ByDriveId(driveID).Items().ByDriveItemId(itemID).Get(ctx, nil)
	if err != nil {
		return connect.FileItem{}, x.fail(ctx, err)
	}
	return itemFrom(driveID, it), nil
}

func (x filesAdapter) Open(ctx context.Context, driveID, itemID string, maxBytes int64) (connect.FileItem, io.ReadCloser, error) {
	meta, err := x.Item(ctx, driveID, itemID)
	if err != nil {
		return meta, nil, err
	}
	if meta.Folder {
		return meta, nil, errors.New("a folder cannot be downloaded")
	}
	if maxBytes > 0 && meta.Size > maxBytes {
		return meta, nil, fmt.Errorf("that file is %d MB, over the %d MB import limit; link it instead", meta.Size>>20, maxBytes>>20)
	}
	data, err := x.client.Drives().ByDriveId(driveID).Items().ByDriveItemId(itemID).Content().Get(ctx, nil)
	if err != nil {
		return meta, nil, x.fail(ctx, err)
	}
	return meta, io.NopCloser(bytes.NewReader(data)), nil
}

func readAll(r io.Reader, size int64) ([]byte, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read the file: %w", err)
	}
	if int64(len(data)) > connect.MaxImport {
		return nil, fmt.Errorf("that file is over the %d MB limit", connect.MaxImport>>20)
	}
	return data, nil
}

// putContent writes bytes to an item, in one request when the file is small and through an upload session when it is
// not.
func (x filesAdapter) putContent(ctx context.Context, driveID, addr string, data []byte) (models.DriveItemable, error) {
	items := x.client.Drives().ByDriveId(driveID).Items().ByDriveItemId(addr)
	if len(data) <= smallUpload {
		it, err := items.Content().Put(ctx, data, nil)
		return it, x.fail(ctx, err)
	}
	body := drives.NewItemItemsItemCreateUploadSessionPostRequestBody()
	props := models.NewDriveItemUploadableProperties()
	props.SetAdditionalData(map[string]any{"@microsoft.graph.conflictBehavior": "replace"})
	body.SetItem(props)
	sess, err := items.CreateUploadSession().Post(ctx, body, nil)
	if err != nil {
		return nil, x.fail(ctx, err)
	}
	// The session URL carries its own authorisation, so no bearer token goes with these requests.
	uploadURL := s(sess.GetUploadUrl())
	u, err := url.Parse(uploadURL)
	gb, _ := url.Parse(x.s.graphBase)
	if err != nil || (u.Scheme != "https" && (gb == nil || u.Host != gb.Host)) {
		return nil, errors.New("microsoft graph: the upload session did not give an https address")
	}
	hc := &http.Client{}
	var last []byte
	for off := 0; off < len(data); off += chunk {
		end := min(off+chunk, len(data))
		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(data[off:end]))
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, end-1, len(data)))
		resp, err := hc.Do(req)
		if err != nil {
			return nil, fmt.Errorf("microsoft graph: upload: %w", err)
		}
		last, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return nil, fmt.Errorf("microsoft graph: upload piece answered %d", resp.StatusCode)
		}
	}
	_ = last
	// The finished item is read back by its id.
	it, err := items.Get(ctx, nil)
	return it, x.fail(ctx, err)
}

// Upload makes an empty file in the folder, then writes its content. The SDK has no path-addressed upload, and
// making the item first means every write after it goes to an id, which Graph resolves without any escaping.
func (x filesAdapter) Upload(ctx context.Context, driveID, folderID, name, mime string, size int64, r io.Reader) (connect.FileItem, error) {
	data, err := readAll(r, size)
	if err != nil {
		return connect.FileItem{}, err
	}
	if folderID == "" {
		id, err := x.rootID(ctx, driveID)
		if err != nil {
			return connect.FileItem{}, err
		}
		folderID = id
	}
	item := models.NewDriveItem()
	item.SetName(ptr(name))
	item.SetFile(models.NewFile())
	item.SetAdditionalData(map[string]any{"@microsoft.graph.conflictBehavior": "rename"})
	made, err := x.client.Drives().ByDriveId(driveID).Items().ByDriveItemId(folderID).Children().Post(ctx, item, nil)
	if err != nil {
		return connect.FileItem{}, x.fail(ctx, err)
	}
	it, err := x.putContent(ctx, driveID, s(made.GetId()), data)
	if err != nil {
		return connect.FileItem{}, err
	}
	if it == nil {
		return itemFrom(driveID, made), nil
	}
	return itemFrom(driveID, it), nil
}

func (x filesAdapter) Replace(ctx context.Context, driveID, itemID, mime string, size int64, r io.Reader) (connect.FileItem, error) {
	data, err := readAll(r, size)
	if err != nil {
		return connect.FileItem{}, err
	}
	it, err := x.putContent(ctx, driveID, itemID, data)
	if err != nil {
		return connect.FileItem{}, err
	}
	return itemFrom(driveID, it), nil
}

func (x filesAdapter) Rename(ctx context.Context, driveID, itemID, name string) (connect.FileItem, error) {
	patch := models.NewDriveItem()
	patch.SetName(ptr(name))
	it, err := x.client.Drives().ByDriveId(driveID).Items().ByDriveItemId(itemID).Patch(ctx, patch, nil)
	if err != nil {
		return connect.FileItem{}, x.fail(ctx, err)
	}
	return itemFrom(driveID, it), nil
}

func (x filesAdapter) Move(ctx context.Context, driveID, itemID, folderID string) (connect.FileItem, error) {
	if folderID == "" {
		id, err := x.rootID(ctx, driveID)
		if err != nil {
			return connect.FileItem{}, err
		}
		folderID = id
	}
	ref := models.NewItemReference()
	ref.SetId(ptr(folderID))
	patch := models.NewDriveItem()
	patch.SetParentReference(ref)
	it, err := x.client.Drives().ByDriveId(driveID).Items().ByDriveItemId(itemID).Patch(ctx, patch, nil)
	if err != nil {
		return connect.FileItem{}, x.fail(ctx, err)
	}
	return itemFrom(driveID, it), nil
}

func (x filesAdapter) Mkdir(ctx context.Context, driveID, folderID, name string) (connect.FileItem, error) {
	if folderID == "" {
		id, err := x.rootID(ctx, driveID)
		if err != nil {
			return connect.FileItem{}, err
		}
		folderID = id
	}
	item := models.NewDriveItem()
	item.SetName(ptr(name))
	item.SetFolder(models.NewFolder())
	item.SetAdditionalData(map[string]any{"@microsoft.graph.conflictBehavior": "rename"})
	it, err := x.client.Drives().ByDriveId(driveID).Items().ByDriveItemId(folderID).Children().Post(ctx, item, nil)
	if err != nil {
		return connect.FileItem{}, x.fail(ctx, err)
	}
	return itemFrom(driveID, it), nil
}

// Delete sends the item to the recycle bin, where the person can restore it.
func (x filesAdapter) Delete(ctx context.Context, driveID, itemID string) error {
	return x.fail(ctx, x.client.Drives().ByDriveId(driveID).Items().ByDriveItemId(itemID).Delete(ctx, nil))
}
