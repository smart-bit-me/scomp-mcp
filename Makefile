BINARY  := scomp-mcp
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
RPM_OUTDIR ?= $(shell pwd)/rpmbuild
SPEC       := packaging/$(BINARY).spec

.PHONY: build build-all test test-race test-cover wire-cost vet srpm rpm clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o $(BINARY) ./cmd/scomp-mcp

build-all:
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64  go build -trimpath -ldflags="-s -w" -o $(BINARY)-linux-amd64   ./cmd/scomp-mcp
	CGO_ENABLED=0 GOOS=linux  GOARCH=arm64  go build -trimpath -ldflags="-s -w" -o $(BINARY)-linux-arm64   ./cmd/scomp-mcp
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64  go build -trimpath -ldflags="-s -w" -o $(BINARY)-darwin-amd64  ./cmd/scomp-mcp
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64  go build -trimpath -ldflags="-s -w" -o $(BINARY)-darwin-arm64  ./cmd/scomp-mcp

test:
	CGO_ENABLED=0 go test ./...

test-race:
	go test -race ./...

wire-cost:
	CGO_ENABLED=0 go test -count=1 -run '^TestWireCost$$' -v ./internal/mcpserver

vet:
	CGO_ENABLED=0 go vet ./...

test-cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

srpm:
	mkdir -p $(RPM_OUTDIR)/SRPMS
	rm -f $(RPM_OUTDIR)/SRPMS/*.src.rpm
	rpkg srpm --spec $(SPEC) --outdir $(RPM_OUTDIR)/SRPMS
	@echo "SRPM: $$(find $(RPM_OUTDIR)/SRPMS -name '*.src.rpm')"

RPM_VERSION := $(shell rpmspec -q --srpm --qf "%{Version}-%{Release}" $(SPEC))
FEDORA_VERSION := $(shell rpm -E %fedora)
rpm: srpm
	mock --no-clean -r fedora-$(FEDORA_VERSION)-x86_64 --resultdir=rpm-results $(RPM_OUTDIR)/SRPMS/$(BINARY)-$(RPM_VERSION).src.rpm

clean:
	rm -rf $(BINARY) $(BINARY)-linux-amd64 $(BINARY)-linux-arm64 $(BINARY)-darwin-amd64 $(BINARY)-darwin-arm64 coverage.out coverage.html vendor/ rpmbuild/ rpm-results/
