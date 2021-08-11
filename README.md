# Teleworker

(under development)

## Development

## Regenerating Protos

With the prerequisites installed and on the `PATH` (see
[gRPC Go Quick Start](https://grpc.io/docs/languages/go/quickstart/)), run:

    protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative worker/workerpb/worker.proto