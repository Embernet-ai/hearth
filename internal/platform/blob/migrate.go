package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
)

// MigrateOptions says how a copy between two stores runs.
type MigrateOptions struct {
	// Apply copies. Without it nothing is written: the run lists both stores,
	// reads both sides of every key they share, and reports what an apply
	// would do.
	Apply bool
	// Workers is how many objects move at once. Zero means four.
	Workers int
	// Progress, when set, is called once per object after it is settled.
	Progress func(key, outcome string)
}

// MigrateReport is what a copy found and did. Every number is counted from the
// stores themselves, not from what the run intended.
type MigrateReport struct {
	SourceObjects int
	SourceBytes   int64
	// Copied were absent from the destination and were written and read back.
	Copied int
	// AlreadyThere were in the destination with identical bytes.
	AlreadyThere int
	// Conflicts are keys present on both sides with different bytes. They are
	// never overwritten: a key is a UUID path, so two different bodies under
	// one key means something is wrong and a person has to look.
	Conflicts []string
	// Failed are keys that could not be copied or read back.
	Failed []string
	// DestinationObjects and DestinationBytes are counted after the run.
	DestinationObjects int
	DestinationBytes   int64
	// ExtraInDestination are keys only the destination holds: objects written
	// since the switch. They are expected, and never touched.
	ExtraInDestination int
	// Verified is true only after an apply, when every source key was read back
	// from the destination with a SHA-256 equal to the source's.
	Verified bool
}

// OK reports whether the run found nothing wrong. A dry run is OK when no
// object conflicts or failed; an apply is OK only when it also Verified.
func (r *MigrateReport) OK(apply bool) bool {
	if len(r.Conflicts) > 0 || len(r.Failed) > 0 {
		return false
	}
	return !apply || r.Verified
}

func hashOf(ctx context.Context, s *Store, key string) (string, int64, error) {
	rc, err := s.Get(ctx, key)
	if err != nil {
		return "", 0, err
	}
	defer rc.Close()
	h := sha256.New()
	n, err := io.Copy(h, rc)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Migrate copies every object in src into dst and proves it: after each write
// the object is read back from dst and its SHA-256 compared with what was read
// from src, and at the end both stores are listed again and counted. It never
// deletes from either side and never overwrites a key dst already holds, so
// running it again is safe, and src is untouched and complete as the fallback.
func Migrate(ctx context.Context, src, dst *Store, opt MigrateOptions) (*MigrateReport, error) {
	if opt.Workers <= 0 {
		opt.Workers = 4
	}
	rep := &MigrateReport{}

	var keys []Object
	if err := src.List(ctx, func(o Object) error {
		keys = append(keys, o)
		rep.SourceObjects++
		rep.SourceBytes += o.Size
		return nil
	}); err != nil {
		return nil, fmt.Errorf("list the source: %w", err)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Key < keys[j].Key })

	var mu sync.Mutex
	settle := func(key, outcome string, fn func()) {
		mu.Lock()
		fn()
		mu.Unlock()
		if opt.Progress != nil {
			opt.Progress(key, outcome)
		}
	}

	work := make(chan Object)
	var wg sync.WaitGroup
	for i := 0; i < opt.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for o := range work {
				if ctx.Err() != nil {
					return
				}
				migrateOne(ctx, src, dst, o, opt.Apply, settle, rep)
			}
		}()
	}
	for _, o := range keys {
		select {
		case work <- o:
		case <-ctx.Done():
		}
	}
	close(work)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return rep, err
	}
	sort.Strings(rep.Conflicts)
	sort.Strings(rep.Failed)

	// Count the destination from the destination itself.
	srcKeys := make(map[string]struct{}, len(keys))
	for _, o := range keys {
		srcKeys[o.Key] = struct{}{}
	}
	if err := dst.List(ctx, func(o Object) error {
		rep.DestinationObjects++
		rep.DestinationBytes += o.Size
		if _, ok := srcKeys[o.Key]; !ok {
			rep.ExtraInDestination++
		}
		return nil
	}); err != nil {
		return rep, fmt.Errorf("list the destination: %w", err)
	}

	if opt.Apply {
		// Every source key must now be in the destination.
		missing := 0
		present := map[string]struct{}{}
		if err := dst.List(ctx, func(o Object) error { present[o.Key] = struct{}{}; return nil }); err != nil {
			return rep, fmt.Errorf("list the destination: %w", err)
		}
		for k := range srcKeys {
			if _, ok := present[k]; !ok {
				missing++
			}
		}
		rep.Verified = missing == 0 && len(rep.Conflicts) == 0 && len(rep.Failed) == 0 &&
			rep.Copied+rep.AlreadyThere == rep.SourceObjects
	}
	return rep, nil
}

func migrateOne(ctx context.Context, src, dst *Store, o Object, apply bool,
	settle func(string, string, func()), rep *MigrateReport) {
	fail := func(why string) {
		settle(o.Key, "failed: "+why, func() { rep.Failed = append(rep.Failed, o.Key) })
	}

	want, n, err := hashOf(ctx, src, o.Key)
	if err != nil {
		fail("read source: " + err.Error())
		return
	}

	ct := "application/octet-stream"
	if so, err := src.Stat(ctx, o.Key); err == nil && so.ContentType != "" {
		ct = so.ContentType
	}
	switch _, err := dst.Stat(ctx, o.Key); {
	case err == nil:
		got, gn, err := hashOf(ctx, dst, o.Key)
		if err != nil {
			fail("read destination: " + err.Error())
			return
		}
		if got == want && gn == n {
			settle(o.Key, "already there", func() { rep.AlreadyThere++ })
			return
		}
		settle(o.Key, "conflict", func() { rep.Conflicts = append(rep.Conflicts, o.Key) })
		return
	case !errors.Is(err, ErrNotFound):
		fail("check destination: " + err.Error())
		return
	}

	if !apply {
		settle(o.Key, "would copy", func() { rep.Copied++ })
		return
	}

	rc, err := src.Get(ctx, o.Key)
	if err != nil {
		fail("read source: " + err.Error())
		return
	}
	_, putErr := dst.Put(ctx, o.Key, rc, n, ct)
	_ = rc.Close()
	if putErr != nil {
		fail("write destination: " + putErr.Error())
		return
	}
	got, gn, err := hashOf(ctx, dst, o.Key)
	if err != nil {
		fail("read back: " + err.Error())
		return
	}
	if got != want || gn != n {
		fail(fmt.Sprintf("read back differs (%d bytes, sha256 %s, wanted %d bytes, %s)", gn, got[:12], n, want[:12]))
		return
	}
	settle(o.Key, "copied", func() { rep.Copied++ })
}
