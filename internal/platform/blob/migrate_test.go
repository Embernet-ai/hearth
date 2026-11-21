package blob_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/platform/blob"
)

// memBackend is an in-memory store that can be told to misbehave, so the
// migration's refusals (a corrupted write, a conflict, an unreadable source) are
// each shown to fail. The backends test above covers the real servers.
type memBackend struct {
	mu      sync.Mutex
	objs    map[string][]byte
	corrupt bool // writes land with one byte flipped
	failGet map[string]bool
}

func newMem() *memBackend { return &memBackend{objs: map[string][]byte{}, failGet: map[string]bool{}} }

func (m *memBackend) Name() string     { return "mem" }
func (m *memBackend) Bucket() string   { return "mem" }
func (m *memBackend) Endpoint() string { return "mem" }
func (m *memBackend) Put(_ context.Context, k string, r io.Reader, _ int64, _ string) (int64, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	if m.corrupt && len(b) > 0 {
		b = append([]byte{}, b...)
		b[0] ^= 0xff
	}
	m.mu.Lock()
	m.objs[k] = b
	m.mu.Unlock()
	return int64(len(b)), nil
}
func (m *memBackend) Get(_ context.Context, k string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failGet[k] {
		return nil, errors.New("disk on fire")
	}
	b, ok := m.objs[k]
	if !ok {
		return nil, blob.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
func (m *memBackend) Stat(_ context.Context, k string) (blob.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[k]
	if !ok {
		return blob.Object{}, blob.ErrNotFound
	}
	return blob.Object{Key: k, Size: int64(len(b))}, nil
}
func (m *memBackend) Remove(_ context.Context, k string) error {
	m.mu.Lock()
	delete(m.objs, k)
	m.mu.Unlock()
	return nil
}
func (m *memBackend) PresignGet(context.Context, string, string, time.Duration) (*url.URL, error) {
	return nil, blob.ErrPresignUnsupported
}
func (m *memBackend) PresignPut(context.Context, string, time.Duration) (*url.URL, error) {
	return nil, blob.ErrPresignUnsupported
}
func (m *memBackend) Probe(context.Context) error { return nil }
func (m *memBackend) List(_ context.Context, fn func(blob.Object) error) error {
	m.mu.Lock()
	keys := make([]string, 0, len(m.objs))
	for k := range m.objs {
		keys = append(keys, k)
	}
	m.mu.Unlock()
	sort.Strings(keys)
	for _, k := range keys {
		if err := fn(blob.Object{Key: k, Size: int64(len(m.objs[k]))}); err != nil {
			return err
		}
	}
	return nil
}

func seed(m *memBackend, n int) {
	for i := 0; i < n; i++ {
		m.objs[fmt.Sprintf("org/%02d", i)] = []byte(strings.Repeat(fmt.Sprint(i), 100+i))
	}
}

func TestMigrateCopiesAndVerifies(t *testing.T) {
	src, dst := newMem(), newMem()
	seed(src, 20)
	rep, err := blob.Migrate(context.Background(), blob.New(src), blob.New(dst), blob.MigrateOptions{Apply: true})
	if err != nil || !rep.OK(true) || rep.Copied != 20 || !rep.Verified || rep.DestinationObjects != 20 {
		t.Fatalf("%+v %v", rep, err)
	}
	if len(src.objs) != 20 {
		t.Fatal("the source lost objects")
	}
}

func TestMigrateCatchesACorruptedWrite(t *testing.T) {
	src, dst := newMem(), newMem()
	seed(src, 5)
	dst.corrupt = true
	rep, err := blob.Migrate(context.Background(), blob.New(src), blob.New(dst), blob.MigrateOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK(true) || rep.Verified || len(rep.Failed) != 5 {
		t.Fatalf("a corrupted copy passed verification: %+v", rep)
	}
}

func TestMigrateNeverOverwritesAConflict(t *testing.T) {
	src, dst := newMem(), newMem()
	seed(src, 3)
	dst.objs["org/01"] = []byte("different bytes")
	rep, err := blob.Migrate(context.Background(), blob.New(src), blob.New(dst), blob.MigrateOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK(true) || len(rep.Conflicts) != 1 || rep.Conflicts[0] != "org/01" {
		t.Fatalf("%+v", rep)
	}
	if string(dst.objs["org/01"]) != "different bytes" {
		t.Fatal("a conflicting object was overwritten")
	}
}

func TestMigrateReportsAnUnreadableSource(t *testing.T) {
	src, dst := newMem(), newMem()
	seed(src, 3)
	src.failGet["org/02"] = true
	rep, err := blob.Migrate(context.Background(), blob.New(src), blob.New(dst), blob.MigrateOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK(true) || len(rep.Failed) != 1 || rep.Copied != 2 {
		t.Fatalf("%+v", rep)
	}
}

func TestMigrateDryRunWritesNothing(t *testing.T) {
	src, dst := newMem(), newMem()
	seed(src, 4)
	rep, err := blob.Migrate(context.Background(), blob.New(src), blob.New(dst), blob.MigrateOptions{})
	if err != nil || rep.Copied != 4 || rep.Verified || len(dst.objs) != 0 {
		t.Fatalf("%+v %v objs=%d", rep, err, len(dst.objs))
	}
}
