# define tool installation path
LOCAL_BIN := $(shell pwd)/bin

# export PATH
export PATH := $(LOCAL_BIN):$(PATH)

# define tool versions
PROTOC_GEN_GO_VERSION := latest
PROTOC_GEN_GO_GRPC_VERSION := latest

all: proto

# install dependencies to bin/
deps:
	@mkdir -p $(LOCAL_BIN)
	@echo "Installing dependencies..."
	GOBIN=$(LOCAL_BIN) go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	GOBIN=$(LOCAL_BIN) go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)

# Generate Proto
proto: deps
	protoc --go_out=. --go_opt=paths=source_relative \
	       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
	       api/raftpb/raft.proto

	protoc --go_out=. --go_opt=paths=source_relative \
	       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
	       api/kvpb/kv.proto

clean:
	rm -f api/raftpb/*.pb.go
	rm -f api/kvpb/*.pb.go
	rm -rf bin/

.PHONY: all deps proto clean
