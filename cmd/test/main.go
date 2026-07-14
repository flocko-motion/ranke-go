// package: main / cli
// type:    cmd
// job:     `test` — a cobra CLI for running the project's customizable test tooling; today one subcommand, `performance`, drives the backend matrix
// limits:  a thin flag→Config→RunMatrix adapter; the matrix logic lives in tests/performance (shared with `go test`)
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/flocko-motion/ranke-go/tests/performance"
)

func main() {
	root := &cobra.Command{
		Use:   "test",
		Short: "Ranke-Graph test tooling",
		Long:  "test runs the project's customizable test tooling.",
	}
	root.AddCommand(performanceCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func performanceCmd() *cobra.Command {
	var (
		size     int
		seed     int64
		access   int
		backends []string
	)
	cmd := &cobra.Command{
		Use:   "performance",
		Short: "Run the storage-backend performance matrix",
		Long: "Generate a deterministic size-N archive into each storage backend and\n" +
			"time the chapters (write / verify / random access), reporting per-step\n" +
			"latency distributions. Backends: mem, fs, sqlite, s3 (a podman MinIO pod).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := performance.Config{
				Size:     size,
				Seed:     seed,
				Access:   access,
				Backends: backends,
			}
			return performance.RunMatrix(cfg, cmd.OutOrStdout(), nil)
		},
	}
	f := cmd.Flags()
	f.IntVar(&size, "size", 100, "generator size (~5×size claims)")
	f.Int64Var(&seed, "seed", 1, "generator seed (fixes every id)")
	f.IntVar(&access, "access", 50, "chapter-3 random accesses")
	f.StringSliceVar(&backends, "backends", nil, "backends to run (comma-separated: mem,fs,sqlite,s3); default all")
	return cmd
}
