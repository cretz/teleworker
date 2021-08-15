package cmd

import (
	"log"
	"os"

	"github.com/cretz/teleworker/worker"
	"github.com/spf13/cobra"
)

// Execute runs the command using program args and exits on failure.
func Execute() {
	// Take shortcut if second argument is child-exec
	if len(os.Args) > 1 && os.Args[1] == "child-exec" {
		if err := worker.ExecLimitedChild(os.Args[2:]); err != nil {
			log.Fatalf("unexpected error: %v", err)
		}
	} else if err := rootCmd().Execute(); err != nil {
		log.Fatal(err)
	}
}

func rootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "teleworker",
		Short: "Worker for running jobs",
	}
	cmd.AddCommand(childExecCmd(), diagCmd(), directExecCmd())
	return cmd
}
