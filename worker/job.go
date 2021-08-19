package worker

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Job represents a running or completed job. Callers should never mutate any
// fields. All visible fields are never changed.
type Job struct {
	// Namespace for the job, can be empty string.
	Namespace string
	// ID of the job, never empty.
	ID string
	// Command of the job, never empty.
	Command string
	// Arguments for the command.
	Args []string
	// If set, the job is limited to this root directory.
	RootFS string
	// Time this job was created.
	CreatedAt time.Time
	// PID of the job while it was running.
	PID int

	doneCtx         context.Context
	doneCancel      context.CancelFunc
	stopCtx         context.Context
	stopCancel      context.CancelFunc
	forceStopCtx    context.Context
	forceStopCancel context.CancelFunc

	// This mutex governs all fields below it
	updateLock sync.RWMutex
	stdout     []byte
	stderr     []byte
	exitCode   *int
	listeners  map[chan<- JobUpdate]struct{}
}

// JobUpdate represents a type of update that can be listened to.
type JobUpdate int

const (
	JobUpdateStdout JobUpdate = iota
	JobUpdateStderr
	JobUpdateExitCode
)

// newJob creates a new Job. This may not populate some fields that may be
// populated by the caller.
func newJob(namespace, id, command string, args ...string) *Job {
	j := &Job{
		Namespace: namespace,
		ID:        id,
		Command:   command,
		Args:      args,
		CreatedAt: time.Now(),
		listeners: map[chan<- JobUpdate]struct{}{},
	}
	// Since these contexts do not have timers, nothing leaks if they are not
	// canceled
	j.doneCtx, j.doneCancel = context.WithCancel(context.Background())
	j.stopCtx, j.stopCancel = context.WithCancel(context.Background())
	j.forceStopCtx, j.forceStopCancel = context.WithCancel(context.Background())
	return j
}

// ReadStdout attempts a non-blocking read of the job output into b from the
// given offset. An error occurs if the offset is beyond the length of the
// output. The byte slice can be empty/nil to only check total output and exit
// code.
//
// This returns the amount of data read (if any), total known amount of data,
// and exit code if the job is complete (or nil if not completed). The exit code
// is the equivalent of calling ExitCode.
//
// Note that if exit code is non-nil here or ExitCode returns a non-nil result,
// the total will never change on successive calls because there is never
// anymore output given to the job after an exit code is present.
func (j *Job) ReadStdout(b []byte, offset int) (read, total int, exitCode *int, err error) {
	return j.readOutput(false, b, offset)
}

// ReadStderr is the equivalent of ReadStdout but for the stderr output. See
// ReadStdout for details on parameters and results.
func (j *Job) ReadStderr(b []byte, offset int) (read, total int, exitCode *int, err error) {
	return j.readOutput(true, b, offset)
}

func (j *Job) readOutput(stderr bool, b []byte, offset int) (read, total int, exitCode *int, err error) {
	j.updateLock.RLock()
	defer j.updateLock.RUnlock()
	out := j.stdout
	if stderr {
		out = j.stderr
	}
	total = len(out)
	exitCode = j.exitCode
	// Only copy to bytes if there are any
	if len(b) > 0 {
		if offset > total {
			err = fmt.Errorf("offset %v out of bounds for length %v", offset, total)
		} else {
			read = copy(b, out[offset:])
		}
	}
	return
}

// AddUpdateListener sets the given channel to receive update type on each
// update. Updates to this channel occur via non-blocking sends, so callers
// should make sure there is enough buffer room for any needed update type or
// the update notification may be missed. Since there are three types of update
// types, the buffer is usually best as >= 3, but can be larger to avoid races
// depending on reader implementation. Caller should never close the channel
// (or at least not until after RemoveUpdateListener is called).
func (j *Job) AddUpdateListener(updates chan<- JobUpdate) {
	j.updateLock.Lock()
	defer j.updateLock.Unlock()
	if j.listeners == nil {
		j.listeners = map[chan<- JobUpdate]struct{}{}
	}
	j.listeners[updates] = struct{}{}
}

// RemoveUpdateListener removes the given channel, that was previously passed to
// AddUpdateListener, from getting updates.
func (j *Job) RemoveUpdateListener(updates chan<- JobUpdate) {
	j.updateLock.Lock()
	defer j.updateLock.Unlock()
	delete(j.listeners, updates)
}

// Stop stops the job if not already stopped and waits for completion or context
// close. This does not error if the job is already stopped. If force is set,
// the job is killed via SIGKILL instead of SIGTERM. If the context closes
// before the job is complete, an error is returned. Otherwise, the exit code is
// returned equivalent to calling ExitCode.
func (j *Job) Stop(ctx context.Context, force bool) (code int, err error) {
	// Cancel the context
	if force {
		j.forceStopCancel()
	} else {
		j.stopCancel()
	}
	// Wait until done
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-j.doneCtx.Done():
		// Exit code can never be nil here
		return *j.ExitCode(), nil
	}
}

// ExitCode returns a non-nil exit code if the job has completed, or nil if it
// is still running. The result will be -1 if the job is completed but an exit
// code could not be determined. If the result is non-nil, there is never more
// output added to the job so the total will never change.
func (j *Job) ExitCode() *int {
	j.updateLock.RLock()
	defer j.updateLock.RUnlock()
	return j.exitCode
}

// updateOutput adds output to the job on the given stream. The byte slice is
// not held by this call and can be reused by caller. This should never be
// called after markDone is called.
func (j *Job) updateOutput(stderr bool, output []byte) {
	j.updateLock.Lock()
	defer j.updateLock.Unlock()
	// Append
	var update JobUpdate
	if stderr {
		j.stderr = append(j.stderr, output...)
		update = JobUpdateStderr
	} else {
		j.stdout = append(j.stdout, output...)
		update = JobUpdateStdout
	}
	// Notify listeners via non-blocking send
	for listener := range j.listeners {
		select {
		case listener <- update:
		default:
		}
	}
}

// markDone puts the exit code on the job. updateOutput should never be called
// after this is called.
func (j *Job) markDone(exitCode int) {
	j.updateLock.Lock()
	defer j.updateLock.Unlock()
	j.exitCode = &exitCode
	j.doneCancel()
	// Notify listeners via non-blocking send
	for listener := range j.listeners {
		select {
		case listener <- JobUpdateExitCode:
		default:
		}
	}
}
