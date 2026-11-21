package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/Embernet-ai/hearth/internal/platform/blob"
	"github.com/Embernet-ai/hearth/internal/platform/config"
)

// runBlobMigrate copies every object from one store into another and proves it.
//
// The destination is the store Hearth is configured for (HEARTH_STORAGE_*,
// HEARTH_S3_*, HEARTH_AZURE_*, HEARTH_GCS_*). The source is read from the same
// names with HEARTH_SRC_ in place of HEARTH_, for example HEARTH_SRC_S3_ENDPOINT
// and HEARTH_SRC_S3_ACCESS_KEY for the MinIO an older install still has.
//
// A dry run is the default. Nothing is ever deleted and nothing the destination
// already holds is overwritten, so the source stays whole as the fallback.
func runBlobMigrate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("blob-migrate", flag.ExitOnError)
	apply := fs.Bool("apply", false, "copy (the default is a dry run that writes nothing)")
	workers := fs.Int("workers", 4, "objects copied at once")
	quiet := fs.Bool("quiet", false, "print only the summary")
	if err := fs.Parse(args); err != nil {
		return err
	}

	srcCfg, err := config.LoadStorage("HEARTH_SRC_")
	if err != nil {
		return fmt.Errorf("source (HEARTH_SRC_*): %w", err)
	}
	dstCfg, err := config.LoadStorage("HEARTH_")
	if err != nil {
		return fmt.Errorf("destination (HEARTH_*): %w", err)
	}
	src, err := blob.Open(ctx, srcCfg)
	if err != nil {
		return fmt.Errorf("open the source: %w", err)
	}
	dst, err := blob.Open(ctx, dstCfg)
	if err != nil {
		return fmt.Errorf("open the destination: %w", err)
	}
	mode := "dry run"
	if *apply {
		mode = "apply"
	}
	fmt.Printf("blob-migrate (%s): %s %s/%s -> %s %s/%s\n", mode,
		src.Backend(), src.InternalEndpoint(), src.Bucket(),
		dst.Backend(), dst.InternalEndpoint(), dst.Bucket())

	rep, err := blob.Migrate(ctx, src, dst, blob.MigrateOptions{
		Apply: *apply, Workers: *workers,
		Progress: func(key, outcome string) {
			if !*quiet {
				fmt.Printf("  %-14s %s\n", outcome, key)
			}
		},
	})
	if rep != nil {
		verb := "would copy"
		if *apply {
			verb = "copied"
		}
		fmt.Printf("source: %d objects, %d bytes\n", rep.SourceObjects, rep.SourceBytes)
		fmt.Printf("%s: %d   already there with identical bytes: %d   conflicts: %d   failed: %d\n",
			verb, rep.Copied, rep.AlreadyThere, len(rep.Conflicts), len(rep.Failed))
		fmt.Printf("destination now: %d objects, %d bytes (%d only in the destination, left alone)\n",
			rep.DestinationObjects, rep.DestinationBytes, rep.ExtraInDestination)
		for _, k := range rep.Conflicts {
			fmt.Printf("  CONFLICT %s\n", k)
		}
		for _, k := range rep.Failed {
			fmt.Printf("  FAILED %s\n", k)
		}
		if *apply {
			fmt.Printf("verified (every source key read back from the destination, SHA-256 equal): %v\n", rep.Verified)
		}
	}
	if err != nil {
		return err
	}
	if !rep.OK(*apply) {
		fmt.Fprintln(os.Stderr, "blob-migrate: not clean, see above; nothing was deleted anywhere")
		os.Exit(1)
	}
	return nil
}
