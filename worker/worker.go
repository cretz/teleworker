// Package worker implements a worker that can run jobs.
package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// Worker represents a worker that can manage jobs.
type Worker struct {
	runner    runner
	hasLimits bool
	// Keyed by namespace, then ID
	jobs     map[string]map[string]*Job
	jobsLock sync.RWMutex

	shutdown     bool
	shutdownLock sync.RWMutex
}

// Config is configuration for a worker.
type Config struct {
	// If nil, jobs will not have any limits placed.
	Limits *JobLimitConfig
}

// StandardConfig is a commonly used configuration for limiting jobs.
var StandardConfig = Config{
	Limits: &JobLimitConfig{
		ResourceLimits: JobResourceLimits{
			// 0.2 cores
			CPUMaxPeriod: 10000,
			CPUMaxQuota:  2000,
			// 50MB
			MemoryMax: 50 * 1024 * 1024,
			// 1MB/s
			DeviceIOMax: map[string]uint64{"": 1 * 1024 * 1024},
		},
		Isolation: JobIsolation{
			PID:     true,
			Network: true,
			Mount:   true,
		},
	},
}

// New creates a new worker from the given configuration. Note, any config
// pointers/references may be mutated internally (e.g. the device io max map).
func New(config Config) (*Worker, error) {
	w := &Worker{hasLimits: config.Limits != nil, jobs: map[string]map[string]*Job{}}
	// Only use limited runner when resource limits are set
	if w.hasLimits {
		var err error
		if w.runner, err = newLimitedRunner(config.Limits); err != nil {
			return nil, fmt.Errorf("creating limited runner: %w", err)
		}
	} else {
		w.runner = newRunner()
	}
	return w, nil
}

// ErrShutdown is returned from worker calls when the worker is shutdown.
var ErrShutdown = errors.New("worker shutdown")

// ErrIDAlreadyExists is returned from Worker.SubmitJob if the ID already
// exists.
var ErrIDAlreadyExists = errors.New("ID already exists")

// GetJob returns a job for the given namespace and ID, or nil with no error if
// not found. This returns ErrShutdown if the worker is shutdown. Callers should
// not mutate any fields on the resulting job.
func (w *Worker) GetJob(namespace, id string) (*Job, error) {
	w.shutdownLock.RLock()
	defer w.shutdownLock.RLock()
	if w.shutdown {
		return nil, ErrShutdown
	}
	w.jobsLock.RLock()
	defer w.jobsLock.RUnlock()
	return w.jobs[namespace][id], nil
}

// SubmitJobOption represents an option for Worker.SubmitJob
type SubmitJobOption func(*Job)

// WithRootFS is a submit job option to set the root filesystem of a job. This
// cannot be set on a worker configured without job limits.
func WithRootFS(root string) SubmitJobOption {
	return func(j *Job) { j.RootFS = root }
}

// SubmitJob submits a job to run on the worker. If the ID is empty one will be
// created, otherwise it must be unique per namespace or ErrIDAlreadyExists is
// returned. Namespace can be empty. This returns ErrShutdown if the worker is
// shutdown. If the job is successfully started, it is returned with PID.
// Otherwise an error is returned.
func (w *Worker) SubmitJob(namespace, id, command string, args []string, opts ...SubmitJobOption) (*Job, error) {
	// Lock shutdown for life of the submission
	w.shutdownLock.RLock()
	defer w.shutdownLock.RUnlock()
	if w.shutdown {
		return nil, ErrShutdown
	}
	// Make unique ID if not there
	if id == "" {
		id = uuid.New().String()
	}
	// Put nil in the map to confirm ID not in use and hold ID spot
	w.jobsLock.Lock()
	_, exists := w.jobs[namespace][id]
	if !exists {
		if w.jobs[namespace] == nil {
			w.jobs[namespace] = map[string]*Job{}
		}
		w.jobs[namespace][id] = nil
	}
	w.jobsLock.Unlock()
	if exists {
		return nil, ErrIDAlreadyExists
	}
	// Remove ID from job map on failure
	success := false
	defer func() {
		if !success {
			w.jobsLock.Lock()
			defer w.jobsLock.Unlock()
			delete(w.jobs[namespace], id)
		}
	}()
	// Create job with options
	job := newJob(namespace, id, command, args...)
	for _, opt := range opts {
		opt(job)
	}
	if !w.hasLimits && job.RootFS != "" {
		return nil, fmt.Errorf("cannot set root FS on non-limited worker")
	}
	// Attempt to start job
	if err := w.runner.start(job); err != nil {
		return nil, fmt.Errorf("starting job: %w", err)
	}
	// Add to map and return
	w.jobsLock.Lock()
	w.jobs[namespace][id] = job
	w.jobsLock.Unlock()
	success = true
	return job, nil
}

// Shutdown stops all jobs via Job.Stop, waits for all jobs to finish or context
// to close. This returns nil if all jobs have completed, or the context error
// otherwise. Regardless of result, once this is called no other calls can be
// used on this worker. This returns ErrShutdown if the worker is already
// shutdown.
func (w *Worker) Shutdown(ctx context.Context, force bool) error {
	// Mark worker as shutdown
	w.shutdownLock.Lock()
	alreadyShutdown := w.shutdown
	w.shutdown = true
	w.shutdownLock.Unlock()
	if alreadyShutdown {
		// TODO(cretz): Probably don't want to do this so that they can call this
		// multiple times
		return ErrShutdown
	}
	w.jobsLock.Lock()
	jobs := w.jobs
	w.jobs = nil
	w.jobsLock.Unlock()
	// If there are no jobs, eagerly return nil
	if len(jobs) == 0 {
		return nil
	}
	// Asynchronously stop all jobs, ignoring errors
	var wg sync.WaitGroup
	for _, jobsByID := range jobs {
		for _, job := range jobsByID {
			job := job
			wg.Add(1)
			go func() {
				defer wg.Done()
				job.Stop(ctx, force)
			}()
		}
	}
	// Wait for complete or context close
	wgDone := make(chan struct{})
	go func() {
		defer close(wgDone)
		wg.Wait()
	}()
	// Wait for context done or context close
	select {
	case <-ctx.Done():
	case <-wgDone:
	}
	// Do another non-blocking read to check for done. We do this separately
	// instead of above just in case context is closed but wait group was marked
	// done also.
	select {
	case <-wgDone:
		return nil
	default:
		return ctx.Err()
	}
}
