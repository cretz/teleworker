package workergrpc

import (
	"context"
	"fmt"
	"time"

	"github.com/cretz/teleworker/worker"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type jobService struct {
	UnimplementedJobServiceServer
	worker *worker.Worker
}

// NewJobServiceServer returns an implementation of JobServiceServer backed by
// the given worker.
func NewJobServiceServer(w *worker.Worker) JobServiceServer { return &jobService{worker: w} }

func (j *jobService) GetJob(ctx context.Context, req *GetJobRequest) (*GetJobResponse, error) {
	// Get job
	job, err := j.getJob(ctx, req.JobId)
	if err != nil {
		return nil, err
	}
	// Convert and return
	pbJob, err := toProtoJob(job, req.IncludeStdout, req.IncludeStderr)
	if err != nil {
		return nil, err
	}
	return &GetJobResponse{Job: pbJob}, nil
}

// getJob fails if job not found.
func (j *jobService) getJob(ctx context.Context, id string) (*worker.Job, error) {
	if id == "" {
		return nil, status.Errorf(codes.InvalidArgument, "job ID required")
	}
	job, err := j.worker.GetJob(namespaceFromContext(ctx), id)
	if err == worker.ErrShutdown {
		return nil, status.Error(codes.FailedPrecondition, "worker shutdown")
	} else if err != nil {
		return nil, err
	} else if job == nil {
		return nil, status.Error(codes.NotFound, "not found")
	}
	return job, nil
}

func namespaceFromContext(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	// No peer info setup up means use empty namespace
	if !ok {
		return ""
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	// No TLS cert means it was not setup to require it, so use empty namespace
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
		return ""
	}
	// Use the first OU
	ous := tlsInfo.State.PeerCertificates[0].Subject.OrganizationalUnit
	if len(ous) == 0 {
		return ""
	}
	return ous[0]
}

func toProtoJob(job *worker.Job, includeStdout, includeStderr bool) (*Job, error) {
	pbJob := &Job{
		Id:        job.ID,
		Command:   append([]string{job.Command}, job.Args...),
		RootFs:    job.RootFS,
		CreatedAt: timestamppb.New(job.CreatedAt),
		Pid:       int64(job.PID),
	}
	// We intentionally obtain the exit code before getting output since it's not
	// atomic. If we get the exit code after, we could have a case where the exit
	// code appears even though all output may not. By getting before we err on
	// the side of no exit code even though just completed before output.
	if exitCode := job.ExitCode(); exitCode != nil {
		pbJob.ExitCode = wrapperspb.Int32(int32(*exitCode))
	}
	var err error
	if includeStdout {
		if pbJob.Stdout, err = allOutput(job.ReadStdout); err != nil {
			return nil, fmt.Errorf("reading stdout: %w", err)
		}
	}
	if includeStderr {
		if pbJob.Stderr, err = allOutput(job.ReadStderr); err != nil {
			return nil, fmt.Errorf("reading stderr: %w", err)
		}
	}
	return pbJob, nil
}

func allOutput(fn func(b []byte, offset int) (read, total int, exitCode *int, err error)) ([]byte, error) {
	// Continually ask until no more left
	var offset int
	buf := make([]byte, 1024)
	var out []byte
	for {
		n, _, _, err := fn(buf, offset)
		if n == 0 || err != nil {
			return out, err
		}
		out = append(out, buf[:n]...)
		offset += n
	}
}

func (j *jobService) SubmitJob(ctx context.Context, req *SubmitJobRequest) (*SubmitJobResponse, error) {
	// Validate
	if len(req.Job.Command) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one command value required")
	} else if req.Job.CreatedAt != nil {
		return nil, status.Error(codes.InvalidArgument, "created at cannot be present on create")
	} else if req.Job.Pid != 0 {
		return nil, status.Error(codes.InvalidArgument, "PID cannot be present on create")
	} else if len(req.Job.Stdout) != 0 {
		return nil, status.Error(codes.InvalidArgument, "stdout cannot be present on create")
	} else if len(req.Job.Stderr) != 0 {
		return nil, status.Error(codes.InvalidArgument, "stderr cannot be present on create")
	} else if req.Job.ExitCode != nil {
		return nil, status.Error(codes.InvalidArgument, "exit code cannot be present on create")
	}
	// Submit, convert, and return
	var submitOpts []worker.SubmitJobOption
	if req.Job.RootFs != "" {
		submitOpts = []worker.SubmitJobOption{worker.WithRootFS(req.Job.RootFs)}
	}
	job, err := j.worker.SubmitJob(
		namespaceFromContext(ctx), req.Job.Id, req.Job.Command[0], req.Job.Command[1:], submitOpts...)
	if err == worker.ErrShutdown {
		return nil, status.Error(codes.FailedPrecondition, "worker shutdown")
	} else if err == worker.ErrIDAlreadyExists {
		return nil, status.Error(codes.AlreadyExists, "job with ID already exists")
	} else if err != nil {
		return nil, err
	}
	pbJob, err := toProtoJob(job, false, false)
	if err != nil {
		return nil, err
	}
	return &SubmitJobResponse{Job: pbJob}, nil
}

func (j *jobService) StopJob(ctx context.Context, req *StopJobRequest) (*StopJobResponse, error) {
	// Get job
	job, err := j.getJob(ctx, req.JobId)
	if err != nil {
		return nil, err
	}
	// If the job is already stopped, error. We accept the race condition where
	// technically it could be stopped before the Stop call is called next.
	if job.ExitCode() != nil {
		return nil, status.Error(codes.FailedPrecondition, "job already stopped")
	}
	// Attempt to stop only for 3 seconds
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := job.Stop(ctx, req.Force); err == context.DeadlineExceeded {
		return nil, status.Error(codes.DeadlineExceeded, "failed stopping job within 3 seconds")
	} else if err != nil {
		return nil, err
	}
	// Convert and return
	pbJob, err := toProtoJob(job, false, false)
	if err != nil {
		return nil, err
	}
	return &StopJobResponse{Job: pbJob}, nil
}

func (j *jobService) StreamJobOutput(req *StreamJobOutputRequest, srv JobService_StreamJobOutputServer) error {
	// Get job
	job, err := j.getJob(srv.Context(), req.JobId)
	if err != nil {
		return err
	}
	// gRPC docs not only say send cannot occur concurrently, but can't even occur
	// on separate goroutines so we use a channel to do all sends on the same
	// goroutine (this one). We do not need to buffer this since we expect all
	// sends to the channel to be bound by the context.
	responseCh := make(chan *StreamJobOutputResponse)
	// Start the streams asynchronously. We expect srv.Context() to be closed on
	// return therefore we don't create our own context.
	var stdoutErrCh chan error
	if !req.GetOnlyStderr() {
		stdoutErrCh = make(chan error, 1)
		go func() { stdoutErrCh <- j.streamOutput(srv, responseCh, job, req.FromBeginning, false /*stderr*/) }()
	}
	var stderrErrCh chan error
	if !req.GetOnlyStdout() {
		stderrErrCh = make(chan error, 1)
		go func() { stderrErrCh <- j.streamOutput(srv, responseCh, job, req.FromBeginning, true /*stderr*/) }()
	}
	// Continue reading until both channels are nil
	for stdoutErrCh != nil || stderrErrCh != nil {
		select {
		case <-srv.Context().Done():
			return srv.Context().Err()
		case resp := <-responseCh:
			if err := srv.Send(resp); err != nil {
				return err
			}
		case err := <-stdoutErrCh:
			if err != nil {
				return err
			}
			// Stdout is done
			stdoutErrCh = nil
		case err := <-stderrErrCh:
			if err != nil {
				return err
			}
			// Stderr is done
			stderrErrCh = nil
		}
	}
	// Both streams completed successfully (or were not started), send exit code
	// and complete. We count on gRPC server stream to send before closing stream
	// completely (assuming client doesn't abnormally terminate).
	return srv.Send(&StreamJobOutputResponse{
		// Exit code will always be present since that's the only way streamOutput
		// closes without error
		Response: &StreamJobOutputResponse_CompletedExitCode{CompletedExitCode: int32(*job.ExitCode())},
	})
}

func (j *jobService) streamOutput(
	srv JobService_StreamJobOutputServer,
	responseCh chan<- *StreamJobOutputResponse,
	job *worker.Job,
	fromBeginning bool,
	stderr bool,
) error {
	readFn := job.ReadStdout
	if stderr {
		readFn = job.ReadStderr
	}
	// Make an eager read with a nil slice to get the initial total
	_, pastTotal, _, err := readFn(nil, 0)
	if err != nil {
		return err
	}
	// If they want from the beginning, read until we have it all.
	// TODO(cretz): I am intentionally immediately starting live after this
	// without potentially waiting for the other stream to be done with the past.
	// We can easily make this ordered if we needed to.
	if fromBeginning {
		b := make([]byte, pastTotal)
		if _, _, _, err := readFn(b, 0); err != nil {
			return err
		}
		msg := &StreamJobOutputResponse{Past: true}
		if stderr {
			msg.Response = &StreamJobOutputResponse_Stderr{Stderr: b}
		} else {
			msg.Response = &StreamJobOutputResponse_Stdout{Stdout: b}
		}
		// Send
		select {
		case <-srv.Context().Done():
			return srv.Context().Err()
		case responseCh <- msg:
		}
	}
	// Start a listener with a buffer of 3 to make sure we can backpressure any
	// single type
	updateCh := make(chan worker.JobUpdate, 3)
	job.AddUpdateListener(updateCh)
	defer job.RemoveUpdateListener(updateCh)
	// Start from the past total
	offset := pastTotal
	buf := make([]byte, 1024)
	for {
		n, _, exitCode, err := readFn(buf, offset)
		if err != nil {
			return err
		}
		offset += n
		if n > 0 {
			// Since we are putting this on a channel for later use, we have to copy
			// the bytes
			b := make([]byte, n)
			copy(b, buf)
			msg := &StreamJobOutputResponse{Past: true}
			if stderr {
				msg.Response = &StreamJobOutputResponse_Stderr{Stderr: b}
			} else {
				msg.Response = &StreamJobOutputResponse_Stdout{Stdout: b}
			}
			// Send
			select {
			case <-srv.Context().Done():
				return srv.Context().Err()
			case responseCh <- msg:
			}
		}
		// If there is an exit code, we're done
		if exitCode != nil {
			return nil
		}
		// Wait for any update. In addition to exit code update, we could only wait
		// for the stdout or stderr we care about, but it is harmless to make extra
		// reads and keeps the code simple to just wait for any update.
		select {
		case <-srv.Context().Done():
			return srv.Context().Err()
		case <-updateCh:
		}
	}
}
