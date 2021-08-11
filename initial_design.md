# Teleworker - Initial Design

This document contains the initial design of Teleworker. It is intentionally brief and limited by trade-offs listed at
the end. Much of this complements the [worker.proto](worker/workerpb/worker.proto) file. While the document does have
implementation details at varying levels of depth, it is acknowledged that they may change during development and are
only valid at the time of this writing.

## Library

Teleworker will have a library in `worker/`. The components are listed below.

### Job Manager

The manager component maintains a set of jobs and has operations to read and submit them. A manager is created with
configuration consisting of an optional `JobLimitsConfig` (see proto) which it simply constructs a job runner with.

All operations accept a context even if the context is unused (e.g. a memory-only get). The operations are listed below.

**get job**

Obtain a job by its ID, then atomically clone and return the protobuf representation of the job.

**stop job**

Stop a job by its ID if not already stopped. Accepts a `force` boolean that will send a `SIGKILL` when true or a
`SIGTERM` when false. This call blocks until the job is stopped or the context is closed. Upon successful stop, the job
is returned akin to how the get job operation does.

**stop jobs**

Same as the stop job operation, but for all jobs. This is used by the CLI for graceful termination. This blocks until
all jobs have stopped or the context is closed.

**stream job output**

Accept a callback (or channel, depends on implementation decisions) to send. Also has a bool `past` parameter that, if
true, will immediately send all past output before sending streaming output. This will block and continue to send output
until the job completes or the context is closed.

### Job

The unexported job contains the protobuf job state as well as any mutexes for locking field access.

### Job Runner

Job runner is an unexported interface for submitting a job. It is abstracted for platform independence. There are two
implementations: a `os/exec`-based platform-independent runner that does not accept job limits, and a linux-only runner
that does accept job limits.

### gRPC Server

A `ServeGRPC` operation will exist that accepts a context, a job manager, a `GrpcServerConfig` (see proto), and an
optional variable number of additional `grpc.ServerOption`s. This call is blocking and the implementation of the gRPC
service will, after security checks, delegate to the manager and convert output and errors to gRPC accordingly.

#### Security

The gRPC server can optionally be protected by TLS and by client mTLS. These are set via `grpc/credentials`-based TLS
configuration. The `GrpcServerConfig` (see proto) contains the server key pair to serve TLS with. The configuration also
contains a CA to verify against client certificates. Finally, the configuration contains a set of "client cert matchers"
for read and write capabilities. A client certificate must satisfy the matcher for a capability to be granted. Clients
with write capabilities are automatically granted read capabilities. See proto for details.

### gRPC Client

A `DialGRPC` operation will exist that accepts a context and a configuration struct with options for the address, CA
certificate to validate the server certificate with, and a key pair to send as the client certificate. This will use the
`grpc/credentials`-based TLS configuration.

### Testing

For the phase 1 (see "Implementation Phases" section), minimal tests will be done to ensure the library works as
documented. Once the gRPC server is implemented in phase 2, a few more end-to-end tests will confirm functionality. The
number of tests is intentionally limited by request.

## CLI

The primary build of the repo is a cobra-based CLI, implemented in `cmd/` and called from `main.go`. The commands are
listed below.

**child-exec**

    child-exec NAMESPACE_ARGS COMMAND...

This is a special command that, from research, may be needed to set network and mount namespace isolation. Basically
this command just limits the Go runtime to single-threaded and then execs the given command with isolation calls. This
should never be invoked by a user directly only internally. The command is likely caught by `init` function instead of a
cobra command, though a cobra command may be present for usage documentation reasons.

**gen-key**

    gen-key [--signer-cert FILE] [--signer-key FILE] [--is-ca] [--ou OU] [--cn CN] FILENAME_SANS_EXT

This command simply generates an ECDSA P-256 key. If `--signer-cert` and `--signer-key` are present, the certificate is
signed with it, otherwise it is self-signed. If `--is-ca` is present, it can be used as a signer key for other
certificates. If `--ou` is present, the organizational unit is set to the value. If `--cn` is present, the common name
is set to the value.

**get**

    get [COMMON_CLIENT_COMMANDS] [--stdout] [--stderr] JOB_ID

This command prints the current job information for the given job ID from the server. If `--stdout` is present, the
stdout is also printed. If `--stderr` is present, the stderr is also printed.

`COMMON_CLIENT_COMMANDS` are a set of commands used for communicating with the gRPC server. These are `--address ADDR`,
`--server-ca-cert FILE`, `--client-cert FILE`, and `--client-key FILE`. `--address` is the IP/host + port to contact the
gRPC server on. `--server-ca-cert` is the file to verify the server certificate against. `--client-cert` and
`--client-key` is the key pair to send as the client certificate for auth.

**serve**

    serve -c/--config CONFIG_FILE

This command starts the job manager and gRPC server using the given config file. The config file is a YAML file that
serializes into `ServerConfig` (see proto). This runs until there is some error or a SIGTERM/SIGINT is received. When
one of those two signals is received, a graceful shutdown of the job manager will be attempted with a short timeout.

**stop**

    stop [COMMON_CLIENT_COMMANDS] [--force] JOB_ID

This command stops a job on the server. The `COMMON_CLIENT_COMMANDS` are the same as in the `get` command. If `--force`
is present, `SIGKILL` is used instead of `SIGTERM`. If the job stops successfully, regardless of status code, this
returns successfully with a printed job like the `get` command.

**submit**

    submit [COMMON_CLIENT_COMMANDS] [--id ID] COMMAND...

This command submits a job to the server. The `COMMON_CLIENT_COMMANDS` are the same as in the `get` command. The `--id`
can optionally be provided to set an ID. The result of this command is a printed job like the `get` command.

**tail**

    tail [COMMON_CLIENT_COMMANDS] [-f/--follow] [--no-past] [--stderr] [--stdout-and-stderr] JOB_ID

This command, by default, prints the existing stdout for the job on the server. The `COMMON_CLIENT_COMMANDS` are the
same as in the `get` command. If `-f/--follow` is present, the command will give all past output then continually stream
new output. If `--no-past` is present (only allowed when `-f/--follow` is present), the continually streamed output will
not begin with all past output. If `--stderr` is present, the output is stderr instead of stdout. If
`--stdout-and-stderr` is present, the output is stderr and stdout (intentionally a wordy flag name as output is in
non-deterministic order).

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
