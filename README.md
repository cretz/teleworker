# Teleworker

(under development, missing tests and documentation)

Teleworker is a library, CLI, and gRPC server for managing the running of executable jobs.

## Usage

## Building

Simply run `go build` from the repository root with a recent version of Go.

### CLI

TODO(cretz): Document CLI

### gRPC Server

TODO(cretz): Document server

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