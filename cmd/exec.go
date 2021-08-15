package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cretz/teleworker/worker"
	"github.com/spf13/cobra"
)

func childExecCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "child-exec",
		Short: "Internal command for applying limits to child executable",
	}
}

func directExecCmd() *cobra.Command {
	var withoutLimits bool
	var root string
	cmd := &cobra.Command{
		Use:   "direct-exec -- COMMAND [ARGS...]",
		Short: "Internal command for applying limits to child executable",
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			config := worker.Config{}
			if !withoutLimits {
				config = worker.StandardConfig
			}
			var opts []worker.SubmitJobOption
			if root != "" {
				opts = append(opts, worker.WithRootFS(root))
			}
			if len(args) == 0 {
				log.Fatal("At least one argument required")
			} else if err := runDirectExec(config, args[0], args[1:], opts...); err != nil {
				log.Fatal(err)
			}
		},
	}
	cmd.Flags().BoolVar(&withoutLimits, "without-limits", false, "Run without any resource limits")
	cmd.Flags().StringVar(&root, "root", "", "Change the root")
	return cmd
}

func runDirectExec(config worker.Config, command string, args []string, opts ...worker.SubmitJobOption) error {
	// Start the worker
	w, err := worker.New(config)
	if err != nil {
		return fmt.Errorf("failed starting worker: %w", err)
	}
	// Submit the job
	job, err := w.SubmitJob("", "", command, args, opts...)
	if err != nil {
		return fmt.Errorf("failed submitting job: %w", err)
	}
	// Add update listener w/ just a buffer of 5
	updateCh := make(chan worker.JobUpdate, 5)
	job.AddUpdateListener(updateCh)
	// Prepare to exit on channel
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	// Continually read until job done or signal
	buf := make([]byte, 1024)
	var stdoutOffset, stderrOffset int
	for {
		// Check for exit code before draining
		exitCode := job.ExitCode()
		// Pipe stdout and stderr
		if stdoutOffset, err = drainOutput(job, false, buf, stdoutOffset); err != nil {
			return err
		} else if stderrOffset, err = drainOutput(job, true, buf, stderrOffset); err != nil {
			return err
		} else if exitCode != nil {
			// We can exit safely because we drained after an exit code appeared
			os.Exit(*exitCode)
			return nil
		}
		// Wait for notification of any kind of update or signal
		select {
		case <-updateCh:
			// We don't care the kind of update, we let any update trigger a re-loop
			continue
		case <-sigCh:
			// Attempt graceful shutdown for a few seconds
			// TODO(cretz): Print that we received a termination signal and are
			// attempting graceful shutdown?
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if code, err := job.Stop(ctx, false); err == nil {
				os.Exit(code)
				return nil
			}
			// Timeout, so we attempt a forced shutdown for a few seconds
			ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if code, err := job.Stop(ctx, true); err == nil {
				os.Exit(code)
				return nil
			}
			// Timeout again, nothing we can do
			return fmt.Errorf("failed shutting down job")
		}
	}
}

func drainOutput(j *worker.Job, stderr bool, buf []byte, startOffset int) (nextOffset int, err error) {
	w := os.Stdout
	if stderr {
		w = os.Stderr
	}
	// Read until there is nothing to read
	var n int
	nextOffset = startOffset
	for {
		n, _, _, err = j.ReadOutput(stderr, buf, nextOffset)
		if err != nil {
			return
		} else if n == 0 {
			return
		}
		// Write it
		if _, err = w.Write(buf[:n]); err != nil {
			return
		}
		nextOffset += n
	}
}
