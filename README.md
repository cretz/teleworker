# Teleworker

Teleworker is a library, CLI, and gRPC server for managing the running of executable jobs.

## Usage

## Building

Simply run `go build` from the repository root with a recent version of Go.

## Walkthrough

This walkthrough assumes Linux using Bash.

Before starting the server or using the client, certificates must be generated. The following commands will generate CA
certificates for the server and client and create server and client auth certificates:


    teleworker gen-cert --is-ca server-ca
    teleworker gen-cert --signer-cert server-ca.crt --signer-key server-ca.key --server-host 127.0.0.1 server
    teleworker gen-cert --is-ca client-ca
    teleworker gen-cert --signer-cert client-ca.crt --signer-key client-ca.key --ou my-job-namespace client

4 key pairs will now be present. Start the server in the background:

    teleworker serve --client-ca-cert client-ca.crt --server-cert server.crt --server-key server.key --without-limits &

Note, `--without-limits` can be removed to use a limited runner if running as root with cgroups and namespace isolation
capabilities. We use without limits to be simple in this walkthrough. The command the server in the background and
prints out something like:

    2001/01/01 00:00:00 Serving on 127.0.0.1:33611

Submit a simple echo command:

    teleworker --address 127.0.0.1:33611 --server-ca-cert server-ca.crt --client-cert client.crt --client-key client.key submit -- echo some-output

Of course replace `127.0.0.1:33611` with the server address. This may output something like:

    id: "1322279f-7ac8-4e20-b74c-12e92847842a"
    command: "echo"
    command: "some-output"
    created_at: {
      seconds: 1234567890
      nanos: 987654321
    }
    pid: 801

For future calls, we will omit those first 4 arguments with `<client-args>`. Now get the job:

    teleworker <client-args> get 1322279f-7ac8-4e20-b74c-12e92847842a

This will output the same as above, but maybe with `exit_code: {}` at the end meaning that it exited successfully. We
can also get the output during the get with `--stdout`:

    teleworker <client-args> get --stdout 1322279f-7ac8-4e20-b74c-12e92847842a

This dumps the same thing, but with `stdout: some-output` at the end. If the job were a long-running job, we could stop
it with:

    teleworker <client-args> stop 1322279f-7ac8-4e20-b74c-12e92847842a

If run with this command, a `job already stopped` error will occur. For long-running jobs, we can tail the output
instead of just getting it:

    teleworker <client-args> tail 1322279f-7ac8-4e20-b74c-12e92847842a

Running against this job dumps stdout and a log saying the job is complete. This would run continuous stdout while the
job continues. `--stderr` can br provided to return stderr instead, or `--stdout-and-stderr` for both in undefined
order.

Now that we're all done, we can kill the server:

    pkill teleworker

## Development

### Testing

Integration tests are present in the [tests/](tests) directory that confirm limit behavior. They can be run while in
that directory via normal `go test`.

The tests need to run in a Linux environment configured for cgroups and namespace isolation.
[Vagrant](https://www.vagrantup.com) can be used for this. Simply run `vagrant up` followed by `vagrant ssh` to create
and start a shell in a VM that can run the tests. Once in there, navigate to `/teleworker/tests` and run `go test`.

### Regenerating Protos

With the prerequisites installed and on the `PATH` (see
[gRPC Go Quick Start](https://grpc.io/docs/languages/go/quickstart/)), run:

    protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative workergrpc/worker.proto