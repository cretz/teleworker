# Teleworker - Initial Design

This document contains the initial design of Teleworker. It is intentionally brief and limited by trade-offs listed near
the end. While the document does have implementation details at varying levels of depth, it is acknowledged that they
may change during development and are only valid at the time of this writing.

## Library

Teleworker will have a library in `worker/`. The components are listed below.

### Job

The job contains the state as well as any mutexes for locking field access. The state has the following information:

* namespace - The namespace for this job for grouping purposes.
* id - The job ID which can be user defined or is a generated v4 UUID if not user defined. This must be unique per
  namespace.
* command - String slice of the original command.
* pid - Integer PID of the command.
* exit code - Unexported integer of the exit code (and accompanying boolean saying whether the job has even completed)
* stdout, stderr - Unexported byte slices for content

In addition to the exported, immutable namespace, id, command, and pid fields, the following operations are present:

* get exit code - Atomically get the exit code and true if completed, or false of not completed.
* get output - Copy output into byte slice parameter, optionally from an offset. Returns amount copied and total current
  amount of output.
* listen output - Accept a send-only channel that is continually notified on any output update until context is closed.
  Note, this is just notifying on update with the new total current amount of output, caller must call get output to 
  retrieve it. This simple approach is chosen based on simplistic requirements compared to a more robust output
  broadcasting method. Blocks until context closed or job complete (return value will clarify which).
* stop - Stop the job if not already stopped. Blocks until context is closed or the job is stopped.

#### Output Management

Based on feedback to ignore memory concerns, the output will be stored as simple stdout and stderr byte slices instead
of a traditional rotating set of buffers. The output for each output type is captured via a pipe and appended to its
respective slice under write lock.

Based on feedback to store the entire output for all time, those wanting to stream output will simply hold cursor
indexes into the byte slices instead of a traditional multi-writer or broadcast-based approach.

### Job Runner

Job runner is an unexported interface for submitting a job. It is abstracted for platform independence. There are two
implementations: a `os/exec`-based platform-independent runner that does not accept job limits/isolation configuration,
and a linux-only runner that does accept those.

#### Resource Limiting

Per job resource limits can be provided to the Linux job runner. A default configuration will be available that
implements all limits with reasonable values. The types of limits are listed below.

**CPU**

This is controlled via per-job cgroup. The following parameters are accepted:

* period microseconds - Amount of CPU to divy up
* quota microseconds - Amount of CPU allowed over the period

**memory**

This is controlled via per-job cgroup. The following parameters are accepted:

* max bytes - Maximum amount of bytes to allow job to use

**IO device**

This is controlled via per-job cgroup. There can be multiple device limits. The following parameters are accepted for
each device limit:

* bytes per second - Amount of bytes per second allowed on the device
* major version - Major version of the device
* minor version - Minor version of the device

#### Namespace Isolation

Per job namespace isolation can be configured for the Linux job runner. A default configuration will be available that
uses all isolations with reasonable values. The types of isolations are listed below.

In order to support isolation, we re-execute ourselves (see the `child-exec` CLI command) but setup the new root path.
In the future advanced network isolation settings could be set for the child as well. To keep code simple and prepare
for a future where the child may have to wait for network setup, if any isolation is enabled the self-re-execution is
performed even if not changing the root mount.

**PID**

This is controlled via the `CLONE_NEWPID` syscall attribute to the command. This is enabled via a boolean.

**network**

This is controlled via `CLONE_NEWNET` syscall attribute to the command. This is currently enabled via a boolean. When
enabled, the default of no network interfaces are made available. In the future, loopback or other interfaces could be
configured.

**mount**

This is controlled via `CLONE_NEWNS` syscall attribute to the command. This accepts a single argument for the new root
filesystem path which is applied via the mount and pivot root syscalls.

### Job Manager

The manager component maintains a set of jobs and has operations to get and submit them. A manager is created with
configuration consisting of an optional job limit configuration which it simply constructs a job runner with. Jobs
are grouped by namespace.

**get job**

Obtain a job by its namespace and ID.

**submit job**

Submit a job. The job namespace is optional (default is empty string namespace), ID is optional (default is generated),
and command is required. No other job fields may be set. Result is a started job with a PID.

### Testing

Based on requirements, tests are minimal. Integration tests will be present as Go unit tests to ensure resource limits
are applied, output is properly captured, and invalid command is properly relayed.

## gRPC Service

A GRPC service implementation will be present in `workergrpc/` that handles authentication and authorization on each RPC
call. A job manager will be provided to the service, and after auth, all RPC calls will simply delegate to it,
translating models in either direction. See the [workergrpc/worker.proto](workergrpc/worker.proto) file for details.

### Authentication

Authentication is done via mutual TLS.

For servers, a "get server credentials" operation will be present that accepts a server key pair (for TLS) and a client
CA certificate (to verify client certificates) that returns an instance of `grpc/credentials.TransportCredentials` for
use as a server option by gRPC server creators.

For clients, a "get client credentials" operation will be present that accepts a client key pair (for auth) and a server
CA certificate (to verify server certificate) that returns an instance of `grpc/credentials.TransportCredentials` for
use as a dial option by gRPC client creators.

### Authorization

Authorization simply isolates jobs by using the client certificate's OU as the job namespace. There are no traditional
authorization checks (i.e. there is no such thing as authorization failure, an empty OU is the empty namespace).

### Testing

Based on requirements, tests are minimal. Integration tests will be present that confirm authentication works and
different certificate OUs properly isolate clients.

## CLI

The primary build of the repo is a cobra-based CLI, implemented in `cmd/` and called from `main.go`. The commands are
listed below.

**child-exec**

    child-exec ROOT_FS COMMAND...

This is a special command to re-execute a child command but with the given root filesystem path set as the root (via
pivot root) before executing the child. If `ROOT_FS` is empty, the root will not be changed. In the future this could
support waiting for a network configured by the parent process.

This command should never be executed by the user. It will likely be handled before the rest of the cobra command
handling and will only have a cobra command entry for usage/documentation purposes.

**gen-key**

    gen-key [--signer-cert FILE] [--signer-key FILE] [--is-ca] [--ou OU] [--cn CN] FILENAME_SANS_EXT

This command simply generates an ECDSA P-256 key. If `--signer-cert` and `--signer-key` are present, the certificate is
signed with it, otherwise it is self-signed. If `--is-ca` is present, it can be used as a signer key for other
certificates. If `--ou` is present, the organizational unit is set to the value. If `--cn` is present, the common name
is set to the value.

**get**

    get [COMMON_CLIENT_FLAGS] [--stdout] [--stderr] JOB_ID

This command prints the current job information for the given job ID from the server. If `--stdout` is present, the
stdout is also printed. If `--stderr` is present, the stderr is also printed.

`COMMON_CLIENT_FLAGS` are a set of commands used for communicating with the gRPC server. These are `--address ADDR`,
`--server-ca-cert FILE`, `--client-cert FILE`, and `--client-key FILE`. `--address` is the IP/host + port to contact the
gRPC server on. `--server-ca-cert` is the file to verify the server certificate against. `--client-cert` and
`--client-key` is the key pair to send as the client certificate for auth.

**serve**

    serve [--address ADDR] --server-cert FILE --server-key FILE --client-ca-cert FILE

This command starts the job manager and gRPC server using the given config. The gRPC server will be bound to `--address`
or defaulted to `127.0.0.1:8080`. This runs until there is some error or a `SIGTERM`/`SIGINT` is received. When one of
those two signals is received, the gRPC server is stopped and a graceful shutdown of the job manager will be attempted
with a short timeout.

To maintain simplicity, this will use a default set of resource limits (see the "Resource Limiting" section) and a
default namespace isolation configuration (see "Namespace Isolation" section).

**stop**

    stop [COMMON_CLIENT_FLAGS] [--force] JOB_ID

This command stops a job on the server. The `COMMON_CLIENT_FLAGS` are the same as in the `get` command. If `--force`
is present, `SIGKILL` is used instead of `SIGTERM`. If the job stops successfully, regardless of status code, this
returns successfully with a printed job like the `get` command.

**submit**

    submit [COMMON_CLIENT_FLAGS] [--id ID] COMMAND...

This command submits a job to the server. The `COMMON_CLIENT_FLAGS` are the same as in the `get` command. The `--id` can
optionally be provided to set an ID. The result of this command is a printed job like the `get` command.

**tail**

    tail [COMMON_CLIENT_FLAGS] [-f/--follow] [--no-past] [--stderr] [--stdout-and-stderr] JOB_ID

This command, by default, prints the existing stdout for the job on the server. The `COMMON_CLIENT_FLAGS` are the same
as in the `get` command. If `-f/--follow` is present, the command will give all past output then continually stream
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

## Changelog

This tracks the changes to this document as discussions were had.

* Initially this document was a simple bulleted list of functionality with details left to the proto. By request, it was
  expanded to a more prose style.
* Initially the plan was to keep it simple with a single package for the library that was able to use the proto models
  and have simple helpers to serve/dial gRPC. Based on feedback, the proto will not be used in the library package and
  another package will be just for gRPC and translation code will be written to translate models in either direction.
  Also due to the fact that the proto cannot be reused for the library models, I have put more details in this document
  that explain for the library what the proto documentation explains for the gRPC side.
* Initially the plan was to have a simple proto representing server config YAML. Based on feedback, the config will be
  handwritten struct instead. Originally the code was gonna be a simple serializer from YAML into the proto model. Based
  on feedback, this will now be collections of CLI flags.
* Initially the plan was to have client authorization be based on OU or CN that would affect whether they could just
  view jobs or submit them also (i.e. traditional role-based authorization). Based on feedback, the goal is actually to
  isolate entire sets of jobs by the certificate presented (i.e. job set isolation). This is why the namespace concept
  was implemented.
* Initially the plan was to document at a high level that output would be captured leaving the Go implementation details
  to the Go code. By request, detail about how this will be written in Go has been added to this document.
* Initially the plan was to document at a high level which resource limits would apply and perform the syscall-level
  research on how best to implement for the implementation itself where tests reside to confirm how best to implement.
  By request, more information will be provided upfront about hopefully how best to implement these (while trying to
  avoid writing upfront code to confirm assumptions).
