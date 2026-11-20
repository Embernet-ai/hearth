package main

// The scheduled backup, as one command.
//
// It could have been a shell script in the CronJob: stamp a filename, run
// backup, run verify, delete the oldest. It is not, because the runtime image
// is distroless and has no shell, no date, and no ls in it. That is a good
// property to keep: the smallest image that can run this application is the one
// with the least in it to be wrong about.
//
// So the loop lives here, where it is also testable and where a failure to
// prune is an error rather than a line of output nobody reads.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/backup"
	"github.com/Embernet-ai/hearth/internal/platform/buildinfo"
)

// archivePrefix and archiveSuffix are what makes a file in the backup directory
// one of ours. Anything else in there is left alone: a directory this deletes
// from is a directory that has to be careful.
const (
	archivePrefix = "hearth-"
	archiveSuffix = ".tar"
	// stampLayout sorts lexicographically in time order, which is what lets the
	// pruning be a sort rather than a stat of every file.
	stampLayout = "2006-01-02T150405Z"
)

func runNightly(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("nightly", flag.ExitOnError)
	dir := fs.String("dir", "", "the directory to write archives into")
	keep := fs.Int("keep", 14, "how many archives to keep")
	_ = fs.Parse(args)
	if *dir == "" {
		return errors.New("nightly needs -dir")
	}
	if *keep < 1 {
		return errors.New("keeping fewer than one archive is not a backup policy")
	}

	pools, store, err := open(ctx, "", "")
	if err != nil {
		return err
	}
	defer pools.Close()

	name := filepath.Join(*dir,
		archivePrefix+time.Now().UTC().Format(stampLayout)+archiveSuffix)

	// Written under a temporary name and renamed at the end, so a pod killed
	// halfway leaves nothing that looks like a backup. A partial archive in the
	// backup directory is worse than a missing one: the missing one is noticed.
	tmp, err := os.CreateTemp(*dir, ".hearth-backup-*")
	if err != nil {
		return fmt.Errorf("create an archive in %s: %w", *dir, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	written, err := backup.Write(ctx, pools.Web, store, buildinfo.Get().Revision, tmp)
	if err != nil {
		return err
	}
	report("backed up", written)

	if _, err := tmp.Seek(0, 0); err != nil {
		return fmt.Errorf("rewind the archive: %w", err)
	}
	// Read straight back, in the same run. A backup nobody has read is a file,
	// and the cheapest moment to find out it is not an archive is now rather
	// than on the morning it is needed.
	checked, err := backup.Verify(ctx, tmp)
	if err != nil {
		return err
	}
	report("verified", checked)

	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("flush the archive: %w", err)
	}
	final := tmp.Name()
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close the archive: %w", err)
	}
	if err := os.Rename(final, name); err != nil {
		return fmt.Errorf("put the archive in place: %w", err)
	}
	fmt.Println("wrote " + name)

	removed, err := prune(*dir, *keep)
	if err != nil {
		return err
	}
	for _, gone := range removed {
		fmt.Println("removed " + gone)
	}
	fmt.Printf("%d archives kept in %s\n", *keep, *dir)
	return nil
}

// prune keeps the newest n archives and removes the rest.
//
// By name, which is by time because the stamp is ISO 8601 with no separators.
// Only files this command would have written: a directory something deletes
// from is a directory to be careful in.
func prune(dir string, keep int) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var archives []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, archivePrefix) && strings.HasSuffix(name, archiveSuffix) {
			archives = append(archives, name)
		}
	}
	sort.Strings(archives)
	if len(archives) <= keep {
		return nil, nil
	}

	var removed []string
	for _, name := range archives[:len(archives)-keep] {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return removed, fmt.Errorf("remove %s: %w", name, err)
		}
		removed = append(removed, name)
	}
	return removed, nil
}
