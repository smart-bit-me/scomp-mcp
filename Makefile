.PHONY: build test test-race vet

build:
	CGO_ENABLED=0 go build -trimpath -o scomp-mcp ./cmd/scomp-mcp

test:
	CGO_ENABLED=0 go test ./...

test-race:
	go test -race ./...

vet:
	CGO_ENABLED=0 go vet ./...
