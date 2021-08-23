//go:build linux
// +build linux

package tests

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/cretz/teleworker/worker"
	"github.com/cretz/teleworker/workergrpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TODO(cretz): Intentionally limited test coverage and intentionally all put
// into single test. A more robust testing impl would test pieces separately to
// verify functionality separately.
func TestServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()
	// Start server
	srv := startServer(t)
	defer srv.Stop()
	// Dial 2 clients
	client1 := dialClient(t, srv, "client1")
	defer client1.Close()
	client2 := dialClient(t, srv, "client2")
	defer client2.Close()
	// Run two simple echo jobs on different clients
	job1 := client1.submitAndWait(t, ctx, "sh", "-c", "echo -n stdout1 && echo -n stderr1 1>&2 && exit 101")
	job2 := client2.submitAndWait(t, ctx, "sh", "-c", "echo -n stdout2 && echo -n stderr2 1>&2 && exit 102")
	// Assert some values about job created
	require.NotEmpty(t, job1.Id)
	require.NotEmpty(t, job1.Command)
	require.NotNil(t, job1.CreatedAt)
	require.NotZero(t, job1.Pid)
	// Confirm getter with output
	getJobResp, err := client1.GetJob(ctx, &workergrpc.GetJobRequest{
		JobId:         job1.Id,
		IncludeStdout: true,
		IncludeStderr: true,
	})
	require.NoError(t, err)
	require.Equal(t, "stdout1", string(getJobResp.Job.Stdout))
	require.Equal(t, "stderr1", string(getJobResp.Job.Stderr))
	require.Equal(t, 101, int(getJobResp.Job.ExitCode.GetValue()))
	// Check streaming
	streamResp, err := client1.StreamJobOutput(ctx, &workergrpc.StreamJobOutputRequest{
		JobId:         job1.Id,
		FromBeginning: true,
	})
	require.NoError(t, err)
	var streamStdout, streamStderr []byte
	var exitCode int
	for {
		msg, err := streamResp.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		switch resp := msg.Response.(type) {
		case *workergrpc.StreamJobOutputResponse_Stdout:
			require.Zero(t, exitCode)
			streamStdout = append(streamStdout, resp.Stdout...)
		case *workergrpc.StreamJobOutputResponse_Stderr:
			require.Zero(t, exitCode)
			streamStderr = append(streamStdout, resp.Stderr...)
		case *workergrpc.StreamJobOutputResponse_CompletedExitCode:
			exitCode = int(resp.CompletedExitCode)
		}
	}
	require.Equal(t, "stdout1", string(streamStdout))
	require.Equal(t, "stderr1", string(streamStderr))
	require.Equal(t, 101, exitCode)
	// Make sure client 2's job ran too
	getJobResp, err = client2.GetJob(ctx, &workergrpc.GetJobRequest{
		JobId:         job2.Id,
		IncludeStdout: true,
		IncludeStderr: true,
	})
	require.NoError(t, err)
	require.Equal(t, "stdout2", string(getJobResp.Job.Stdout))
	require.Equal(t, "stderr2", string(getJobResp.Job.Stderr))
	require.Equal(t, 102, int(getJobResp.Job.ExitCode.GetValue()))
	// But if client 1 tries to access client 2, it gets a not found
	_, err = client1.GetJob(ctx, &workergrpc.GetJobRequest{
		JobId:         job2.Id,
		IncludeStdout: true,
		IncludeStderr: true,
	})
	require.Equal(t, codes.NotFound, status.Code(err))
	// Make sure a third client not using the client CA fails (we change the
	// server to pretend like the server CA is the client CA)
	srv.clientCACert = srv.serverCACert
	srv.clientCAKey = srv.serverCAKey
	client3 := dialClient(t, srv, "client3")
	defer client3.Close()
	_, err = client3.GetJob(ctx, &workergrpc.GetJobRequest{JobId: "some id"})
	require.Equal(t, codes.Unavailable, status.Code(err))
}

type server struct {
	*grpc.Server
	addr         string
	serverCACert []byte
	serverCAKey  []byte
	clientCACert []byte
	clientCAKey  []byte
}

func startServer(t *testing.T) *server {
	// Create server CA and server cert
	serverCACert, serverCAKey, err := workergrpc.GenerateCertificate(workergrpc.GenerateCertificateConfig{
		CA: true,
	})
	require.NoError(t, err)
	cert, key, err := workergrpc.GenerateCertificate(workergrpc.GenerateCertificateConfig{
		SignerCert: serverCACert,
		SignerKey:  serverCAKey,
		ServerHost: "127.0.0.1",
	})
	require.NoError(t, err)
	// Create client CA
	clientCACert, clientCAKey, err := workergrpc.GenerateCertificate(workergrpc.GenerateCertificateConfig{
		CA: true,
	})
	require.NoError(t, err)
	// Create non-limited worker
	w, err := worker.New(worker.Config{})
	require.NoError(t, err)
	// Create server
	creds, err := workergrpc.MTLSServerCredentials(clientCACert, cert, key)
	require.NoError(t, err)
	srv := grpc.NewServer(grpc.Creds(creds))
	workergrpc.RegisterJobServiceServer(srv, workergrpc.NewJobServiceServer(w))
	l, err := net.Listen("tcp", "127.0.0.1:")
	require.NoError(t, err)
	go srv.Serve(l)
	return &server{
		Server:       srv,
		addr:         l.Addr().String(),
		serverCACert: serverCACert,
		serverCAKey:  serverCAKey,
		clientCACert: clientCACert,
		clientCAKey:  clientCAKey,
	}
}

type client struct {
	*grpc.ClientConn
	workergrpc.JobServiceClient
}

func dialClient(t *testing.T, s *server, ouNamespace string) *client {
	// Create client cert
	cert, key, err := workergrpc.GenerateCertificate(workergrpc.GenerateCertificateConfig{
		SignerCert: s.clientCACert,
		SignerKey:  s.clientCAKey,
		OU:         ouNamespace,
	})
	require.NoError(t, err)
	// Dial
	creds, err := workergrpc.MTLSClientCredentials(s.serverCACert, cert, key)
	require.NoError(t, err)
	conn, err := grpc.Dial(s.addr, grpc.WithTransportCredentials(creds))
	require.NoError(t, err)
	return &client{ClientConn: conn, JobServiceClient: workergrpc.NewJobServiceClient(conn)}
}

func (c *client) submitAndWait(t *testing.T, ctx context.Context, command ...string) *workergrpc.Job {
	resp, err := c.SubmitJob(ctx, &workergrpc.SubmitJobRequest{Job: &workergrpc.Job{Command: command}})
	require.NoError(t, err)
	// Check for completion every 100ms
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			require.NoError(t, ctx.Err())
		case <-ticker.C:
			job, err := c.GetJob(ctx, &workergrpc.GetJobRequest{JobId: resp.Job.Id})
			require.NoError(t, err)
			if job.Job.ExitCode != nil {
				return job.Job
			}
		}
	}
}
