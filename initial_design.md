# Teleworker - Initial Design

This document contains the initial design of Teleworker. It is intentionally brief and limited by trade-offs listed at
the end. Much of this complements the [worker.proto](worker/workerpb/worker.proto) file. While the document does have
implementation details at varying levels of depth, it is acknowledged that they may change during development and are
only valid at the time of this writing.

## Library

Teleworker will have a library in `worker/`. It will have the following features:

* A `Manager` that accepts config with `JobLimits` (see [worker.proto](worker/workerpb/worker.proto)) with the following
  features:
  * Operations (all context-bounded):
    * Get job - get a job by its ID, clone its proto atomically, and return
    * Submit job - submit a proto job, setting UUID v4 ID if not set by caller, confirms it doesn't already exist by ID,
      and submits to job runner
    * Stop job - stop a job by its ID (using SIGKILL if force bool is true) and block until stopped or context done
    * Stop jobs - stop all running jobs (used by CLI) and block until stopped or context done
    * Stream job output - provide a callback to send byte arrays (callback easy to use with gRPC stream send but may
      switch to accepting send-only channel depending on implementation), returns with exit code if job completes
      * Based on requirements discussion, there is no limit to byte chunk size
  * Based on requirements discussion, there is no limit to the number of jobs stored, completed or otherwise
  * See the [worker.proto](worker/workerpb/worker.proto) service for more details on inputs and outputs
* A `job` which embeds the protobuf `Job` and internally atomically updates itself based on updates
  * Based on requirements discussion, there is no limit to the number of output bytes stored
  * May have `chan struct{}` done channel for others to listen to (will determine during implementation)
  * If we want to properly order stdout and stderr, may have combined output chunks with each chunk containing output
    type instead of byte slice per output type (will determine during implementation, will cause proto to change)
* A `jobRunner` interface for submitting jobs that is backed by a Linux-specific impl
  * Accepts jobs to run and maintains state on the job atomically
* A `ServeGRPC` that accepts a context, manager, gRPC server config (see [worker.proto](worker/workerpb/worker.proto)),
  and  `grpc.ServerOption`s and then blocks until context complete or error
  * Service implementation authenticates client certificates if configured to do so
  * Each call authorizes current client certificate if configured to do so
  * Once authed, calls delegate to the manager
* A `DialGRPC` call that accepts a context and a configuration struct (with address, CA cert to validate server cert,
  and client cert/key for auth) and then returns an interface that combines the service client a `Close` for closing the
  connection.

A couple of manager/job-level tests will be written to confirm phase 1 is working (see "Implementation Phases" section).
The primary tests, intentionally limited to only a couple, will be end-to-end via gRPC as part of phase 2.

## CLI

The primary build of the repo is a cobra-based CLI, implemented in `cmd/` and called from `main.go`, with the following
commands:

* `child-exec` - Special command that, from research, may be needed to set network and mount namespace isolation.
  Basically just limits the Go runtime to single-threaded and then execs the given command with isolation calls. This
  should never be invoked by a user directly (likely caught by `init` function instead of a cobra command, though will
  still have a cobra command for usage documentation reasons).
* `gen-key FILE_SANS_EXT` - Simple ECDSA P-256 key generator. Accepts `--signer-cert` and `--signer-key` files to sign
  with, or is self-signed. Accepts `--is-ca` if expected to be used as CA and signer. Accepts `--ou` for the
  organizational unit and accepts `--cn` for the common name. Writes to `<FILE_SANS_EXT>.crt` and `<FILE_SANS_EXT>.key`.
* `get JOB_ID` - Accepts client CLI args (address, server CA, client cert/key) and dumps the job info sans output. If
  `--stderr` and/or `--stdout` are present, they are dumped as well.
* `serve` - Accepts a `--config` (`-c`) that is a YAML file parsed into `ServerConfig` from
  [worker.proto](worker/workerpb/worker.proto). This starts a manager and starts the gRPC server and waits for SIGTERM
  or SIGINT. Upon signal, attempts to stop all jobs in the manager and waits until all stopped
* `stop JOB_ID` - Accepts same client CLI args as `get` and performs a job stop on the gRPC service
* `submit COMMAND...` - Accepts same client CLI args as `get` and submits the command, dumps the job info, and exits
* `tail JOB_ID` - Accepts same client CLI args as `get` and prints job output. Accepts `-f/--follow` to continually
  receive live output. Accepts `--no-past` to not show past output, only new output (only valid in combination with
  `-f`). By default only dumps stdout. Accepts `--stderr` to only dump stderr or `--stdout-and-stderr` (intentionally
  wordy) to dump combined output in non-deterministic order. If `-f` is set, completes when job completes and always has
  success exit code regardless of what job exit code is.

## Implementation Phases

* Phase 0 - This document
* Phase 1 - The library (except the gRPC client/server) and a couple minimal unit tests
* Phase 2 - The gRPC part of the library, the CLI, a couple end-to-end tests, and README documentation (including
  example configuration and example certs and a simple quick-start for using them)

## Trade-offs

* Only using a single module at the root which means users of the library will be forced into cobra dependency. Could
  make the CLI a sub-module.
* Per requirements discussion, there is no limit to the number of output bytes stored. This greatly simplifies
  implementation but obviously has memory flaws.
* Per requirements discussion, there is no limit to the number of jobs stored.
* Per requirements discussion, `libcontainer` or other dependency cannot be used, direct syscalls for containerization
  are required.
* To keep implementation simple, no metrics or advanced logging will be implemented.
* To keep implementation simple, the gRPC service impl and server/client auth options are not exported. In a real
  library environment, we'd make the service and client easier to use with existing gRPC connections.
* There is no requirement for listing jobs.
* By request, there will not be many tests.
