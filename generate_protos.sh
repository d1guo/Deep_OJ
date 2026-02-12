#!/bin/bash
set -e

# Generate Go Protoc
echo "Generating Go protobuf..."
docker run --rm -v $(pwd):/workspace -w /workspace golang:1.21-alpine sh -c "
    apk add --no-cache protobuf-dev git && \
    go install google.golang.org/protobuf/cmd/protoc-gen-go@latest && \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest && \
    mkdir -p go/pb && \
    protoc --proto_path=proto --go_out=go/pb --go_opt=paths=source_relative \
           --go-grpc_out=go/pb --go-grpc_opt=paths=source_relative \
           proto/judge.proto
"

# 做完了
echo "Done."
