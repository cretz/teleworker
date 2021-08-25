package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/cretz/teleworker/workergrpc"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/prototext"
)

type clientFlags struct {
	address      string
	serverCACert string
	clientCert   string
	clientKey    string
}

func (c *clientFlags) applyFlags(flags *pflag.FlagSet) {
	flags.StringVar(&c.address, "address", "", "Required address the server is listening on")
	flags.StringVar(&c.serverCACert, "server-ca-cert", "", "Required CA certificate file to verify server certificates")
	flags.StringVar(&c.clientCert, "client-cert", "", "Required client certificate file to send for auth")
	flags.StringVar(&c.clientKey, "client-key", "", "Required client key file to send for auth")
}

func (c *clientFlags) dialClient() (*grpc.ClientConn, workergrpc.JobServiceClient, error) {
	if c.address == "" {
		return nil, nil, fmt.Errorf("address required")
	} else if c.serverCACert == "" {
		return nil, nil, fmt.Errorf("server CA cert required")
	} else if c.clientCert == "" {
		return nil, nil, fmt.Errorf("client cert required")
	} else if c.clientKey == "" {
		return nil, nil, fmt.Errorf("client key required")
	}
	// Load cert/key files
	serverCACert, err := os.ReadFile(c.serverCACert)
	if err != nil {
		return nil, nil, fmt.Errorf("reading server CA cert: %w", err)
	}
	clientCert, err := os.ReadFile(c.clientCert)
	if err != nil {
		return nil, nil, fmt.Errorf("reading client cert: %w", err)
	}
	clientKey, err := os.ReadFile(c.clientKey)
	if err != nil {
		return nil, nil, fmt.Errorf("reading client key: %w", err)
	}
	creds, err := workergrpc.MTLSClientCredentials(serverCACert, clientCert, clientKey)
	if err != nil {
		return nil, nil, fmt.Errorf("loading credentials: %w", err)
	}
	conn, err := grpc.Dial(c.address, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, nil, fmt.Errorf("dialing server: %w", err)
	}
	return conn, workergrpc.NewJobServiceClient(conn), nil
}

func getCmd() *cobra.Command {
	var req workergrpc.GetJobRequest
	var clientFlags clientFlags
	cmd := &cobra.Command{
		Use:          "get JOB_ID",
		Short:        "Get job by its ID",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, client, err := clientFlags.dialClient()
			if err != nil {
				return err
			}
			defer conn.Close()
			req.JobId = args[0]
			resp, err := client.GetJob(cmd.Context(), &req)
			if err != nil {
				return fmt.Errorf("getting job: %w", err)
			}
			// Remove stdout/stderr
			stdout, stderr := resp.Job.Stdout, resp.Job.Stderr
			resp.Job.Stdout, resp.Job.Stderr = nil, nil
			// Dump job (this automatically has a newline appended)
			fmt.Print(prototext.Format(resp.Job))
			if req.IncludeStdout {
				fmt.Printf("stdout: %v\n", strings.TrimSpace(string(stdout)))
			}
			if req.IncludeStderr {
				fmt.Printf("stderr: %s\n", strings.TrimSpace(string(stderr)))
			}
			return nil
		},
	}
	clientFlags.applyFlags(cmd.Flags())
	cmd.Flags().BoolVar(&req.IncludeStdout, "stdout", false, "Dump stdout as trimmed string")
	cmd.Flags().BoolVar(&req.IncludeStderr, "stderr", false, "Dump stderr as trimmed string")
	return cmd
}

func stopCmd() *cobra.Command {
	var req workergrpc.StopJobRequest
	var clientFlags clientFlags
	cmd := &cobra.Command{
		Use:          "stop JOB_ID",
		Short:        "Stop job by its ID",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, client, err := clientFlags.dialClient()
			if err != nil {
				return err
			}
			defer conn.Close()
			req.JobId = args[0]
			// Stop in background
			errCh := make(chan error, 1)
			jobCh := make(chan *workergrpc.Job, 1)
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			go func() {
				if resp, err := client.StopJob(ctx, &req); err != nil {
					errCh <- err
				} else {
					jobCh <- resp.Job
				}
			}()
			// Wait for complete or signal
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
			select {
			case err := <-errCh:
				return fmt.Errorf("stopping job: %w", err)
			case job := <-jobCh:
				fmt.Println(prototext.Format(job))
				return nil
			case <-sigCh:
				return fmt.Errorf("signal received, cancelling stop")
			}
		},
	}
	clientFlags.applyFlags(cmd.Flags())
	cmd.Flags().BoolVar(&req.Force, "force", false, "Send SIGKILL instead of SIGTERM")
	return cmd
}

func submitCmd() *cobra.Command {
	req := &workergrpc.SubmitJobRequest{Job: &workergrpc.Job{}}
	var clientFlags clientFlags
	cmd := &cobra.Command{
		Use:          "submit COMMAND [ARGS...]",
		Short:        "Submit a command",
		Args:         cobra.MinimumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, client, err := clientFlags.dialClient()
			if err != nil {
				return err
			}
			defer conn.Close()
			req.Job.Command = args
			// Submit and dump result
			resp, err := client.SubmitJob(cmd.Context(), req)
			if err != nil {
				return fmt.Errorf("submitting job: %w", err)
			}
			fmt.Println(prototext.Format(resp.Job))
			return nil
		},
	}
	clientFlags.applyFlags(cmd.Flags())
	cmd.Flags().StringVar(&req.Job.Id, "id", "", "Set the job ID, otherwise it is generated")
	cmd.Flags().StringVar(&req.Job.RootFs, "root-fs", "", "Root filesystem to limit to")
	return cmd
}

func tailCmd() *cobra.Command {
	var noPast, stderr, stdoutAndStderr bool
	var clientFlags clientFlags
	cmd := &cobra.Command{
		Use:          "tail JOB_ID",
		Short:        "Tail job output",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, client, err := clientFlags.dialClient()
			if err != nil {
				return err
			}
			defer conn.Close()
			// Start stream
			req := &workergrpc.StreamJobOutputRequest{
				JobId:         args[0],
				FromBeginning: !noPast,
			}
			if stderr {
				if stdoutAndStderr {
					return fmt.Errorf("cannot provide stderr and stdout-and-stderr")
				}
				req.StreamLimit = &workergrpc.StreamJobOutputRequest_OnlyStderr{OnlyStderr: true}
			} else if !stdoutAndStderr {
				req.StreamLimit = &workergrpc.StreamJobOutputRequest_OnlyStdout{OnlyStdout: true}
			}
			// Start stream
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			stream, err := client.StreamJobOutput(ctx, req)
			if err != nil {
				return fmt.Errorf("starting stream: %w", err)
			}
			// Dump output in background
			errCh := make(chan error, 1)
			go func() {
				for {
					resp, err := stream.Recv()
					if err != nil {
						errCh <- err
						return
					}
					switch resp := resp.Response.(type) {
					case *workergrpc.StreamJobOutputResponse_Stdout:
						// TODO(cretz): We accept problems here with multibyte charsets
						// where the byte result may break mid-rune
						fmt.Print(string(resp.Stdout))
					case *workergrpc.StreamJobOutputResponse_Stderr:
						fmt.Print(string(resp.Stderr))
					case *workergrpc.StreamJobOutputResponse_CompletedExitCode:
						errCh <- nil
						return
					}
				}
			}()
			// Wait for complete or signal
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
			select {
			case err := <-errCh:
				if err != nil {
					return fmt.Errorf("reading stream: %w", err)
				}
				log.Printf("Job complete")
			case <-sigCh:
			}
			return nil
		},
	}
	clientFlags.applyFlags(cmd.Flags())
	cmd.Flags().BoolVar(&noPast, "no-past", false, "Do not include past output, only live output")
	cmd.Flags().BoolVar(&stderr, "stderr", false, "Only stderr output instead of default stdout")
	cmd.Flags().BoolVar(&stdoutAndStderr, "stdout-and-stderr", false,
		"Both stdout and stderr output (in undefined order)")
	return cmd
}
