package cmd

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cretz/teleworker/worker"
	"github.com/cretz/teleworker/workergrpc"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
)

func serveCmd() *cobra.Command {
	var address string
	var clientCACert, serverCert, serverKey string
	var withoutLimits bool
	cmd := &cobra.Command{
		Use:          "serve",
		Short:        "Start gRPC server",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if clientCACert == "" {
				return fmt.Errorf("client CA cert required")
			} else if serverCert == "" {
				return fmt.Errorf("server cert required")
			} else if serverKey == "" {
				return fmt.Errorf("server key required")
			}
			// Load cert/key files
			clientCACertBytes, err := os.ReadFile(clientCACert)
			if err != nil {
				return fmt.Errorf("reading client CA cert: %w", err)
			}
			serverCertBytes, err := os.ReadFile(serverCert)
			if err != nil {
				return fmt.Errorf("reading server cert: %w", err)
			}
			serverKeyBytes, err := os.ReadFile(serverKey)
			if err != nil {
				return fmt.Errorf("reading server key: %w", err)
			}
			creds, err := workergrpc.MTLSServerCredentials(clientCACertBytes, serverCertBytes, serverKeyBytes)
			if err != nil {
				return fmt.Errorf("loading credentials: %w", err)
			}
			// Create worker
			var w *worker.Worker
			if withoutLimits {
				w, err = worker.New(worker.Config{})
			} else {
				w, err = worker.New(worker.StandardConfig)
			}
			if err != nil {
				return fmt.Errorf("starting worker: %w", err)
			}
			// Force shutdown for one second ignoring error on abnormal close
			defer func() {
				ctx, cancel := context.WithTimeout(cmd.Context(), 1*time.Second)
				defer cancel()
				w.Shutdown(ctx, true)
			}()
			// Serve in background
			srv := grpc.NewServer(grpc.Creds(creds))
			defer srv.Stop()
			workergrpc.RegisterJobServiceServer(srv, workergrpc.NewJobServiceServer(w))
			// This listener is closed before srv.Serve returns below
			l, err := net.Listen("tcp", address)
			if err != nil {
				return fmt.Errorf("listening to address: %w", err)
			}
			serveErrCh := make(chan error, 1)
			go func() { serveErrCh <- srv.Serve(l) }()
			log.Printf("Serving on %v", l.Addr().String())
			// Wait for server error or signal
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
			select {
			case err := <-serveErrCh:
				return fmt.Errorf("serving service: %w", err)
			case <-sigCh:
				log.Printf("Termination signal received, attempting shutdown")
				ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
				defer cancel()
				err := w.Shutdown(ctx, false)
				if err == nil {
					return nil
				}
				log.Printf("Shutdown failed with %v, attempting forced shutdown", err)
				// Timeout, so we attempt a forced shutdown for a few seconds
				ctx, cancel = context.WithTimeout(cmd.Context(), 3*time.Second)
				defer cancel()
				if err := w.Shutdown(ctx, true); err != nil {
					return fmt.Errorf("forced shutdown: %w", err)
				}
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&address, "address", "127.0.0.1:", "Address to listen on")
	cmd.Flags().StringVar(&clientCACert, "client-ca-cert", "", "Required CA certificate file to verify client certificates")
	cmd.Flags().StringVar(&serverCert, "server-cert", "", "Required server certificate file to present to clients")
	cmd.Flags().StringVar(&serverKey, "server-key", "", "Required server key file for server auth")
	cmd.Flags().BoolVar(&withoutLimits, "without-limits", false, "Run without any resource limits")
	return cmd
}
