.PHONY: build test test-unit test-integration test-integration-report lint fmt vet docker-build docker-up docker-down clean openapi \
       build-binaries build-release deb-watchdog deb-peel deb-master deb-cli deb-all deb-all-arch

# ── Versioning ──────────────────────────────────────────
VERSION    ?= $(shell git describe --tags --always 2>/dev/null | sed 's/^v//')
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
DEB_VERSION = $(shell echo "$(VERSION)" | sed 's/-\([0-9]*\)-g/+\1.g/')
LDFLAGS    := -s -w \
  -X github.com/ptorbus/zester/internal/version.Version=$(VERSION) \
  -X github.com/ptorbus/zester/internal/version.GitCommit=$(GIT_COMMIT) \
  -X github.com/ptorbus/zester/internal/version.BuildDate=$(BUILD_DATE)
GOARCH     ?= amd64
NFPM       := $(shell command -v nfpm 2>/dev/null || echo $(shell go env GOPATH)/bin/nfpm)

# ── Build ────────────────────────────────────────────────
build:                                    ## Compile all packages
	go build ./...

build-binaries:                           ## Build all binaries to bin/ (local dev)
	CGO_ENABLED=0 go build -o bin/zester-master ./cmd/zester-master
	CGO_ENABLED=0 go build -o bin/zester-peel ./cmd/zester-peel
	CGO_ENABLED=0 go build -o bin/zester ./cmd/zester
	CGO_ENABLED=0 go build -o bin/zester-watchdog ./cmd/zester-watchdog
	CGO_ENABLED=0 go build -o bin/zester-cli ./playground/zester-cli

build-release:                            ## Build Linux binaries with version injection
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build -ldflags "$(LDFLAGS)" -o bin/release/zester-master ./cmd/zester-master
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build -ldflags "$(LDFLAGS)" -o bin/release/zester-peel ./cmd/zester-peel
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build -ldflags "$(LDFLAGS)" -o bin/release/zester ./cmd/zester
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build -ldflags "$(LDFLAGS)" -o bin/release/zester-watchdog ./cmd/zester-watchdog
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build -ldflags "$(LDFLAGS)" -o bin/release/zester-migrate ./cmd/zester-migrate

# ── Packages ────────────────────────────────────────────
$(NFPM):
	go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest

deb-watchdog: build-release $(NFPM)       ## Build zester-watchdog .deb
	@mkdir -p dist
	VERSION=$(DEB_VERSION) GOARCH=$(GOARCH) $(NFPM) package -p deb -f packaging/nfpm-watchdog.yaml -t dist/

deb-peel: build-release $(NFPM)           ## Build zester-peel .deb
	@mkdir -p dist
	VERSION=$(DEB_VERSION) GOARCH=$(GOARCH) $(NFPM) package -p deb -f packaging/nfpm-peel.yaml -t dist/

deb-master: build-release $(NFPM)         ## Build zester-master .deb
	@mkdir -p dist
	VERSION=$(DEB_VERSION) GOARCH=$(GOARCH) $(NFPM) package -p deb -f packaging/nfpm-master.yaml -t dist/

deb-cli: build-release $(NFPM)            ## Build zester CLI .deb
	@mkdir -p dist
	VERSION=$(DEB_VERSION) GOARCH=$(GOARCH) $(NFPM) package -p deb -f packaging/nfpm-cli.yaml -t dist/

deb-all: deb-watchdog deb-peel deb-master deb-cli  ## Build all .deb packages

deb-all-arch:                             ## Build all .deb packages for amd64 + arm64
	$(MAKE) deb-all GOARCH=amd64
	$(MAKE) deb-all GOARCH=arm64

# ── Test ─────────────────────────────────────────────────
test: test-unit                           ## Run unit tests (default)

test-unit:                                ## Run unit tests only
	go test -count=1 ./...

test-integration:                         ## Run integration tests (requires Docker)
	go test -v -tags integration -count=1 -timeout 10m ./integration/

test-integration-report:                  ## Run integration tests and generate HTML report
	go test -v -tags integration -count=1 -timeout 10m -json ./integration/ \
		| $(shell go env GOPATH)/bin/go-test-report -o integration-report.html -t "Zester Integration Tests"
	@echo "Report written to integration-report.html"


test-all: test-unit test-integration      ## Run all tests

openapi:                                  ## Validate embedded OpenAPI artifacts
	GOCACHE=/tmp/go-build go test -count=1 ./pkg/masterapi -run TestOpenAPIArtifactsEmbedded

# ── Code Quality ─────────────────────────────────────────
fmt:                                      ## Format all Go source files
	gofmt -w .

vet:                                      ## Run go vet
	go vet ./...

lint: vet                                 ## Run linters
	@which golangci-lint > /dev/null 2>&1 || echo "golangci-lint not installed"
	@which golangci-lint > /dev/null 2>&1 && golangci-lint run ./...

# ── Docker ───────────────────────────────────────────────
docker-build:                             ## Build Docker images
	docker compose build

docker-up: docker-build                   ## Start playground stack
	docker compose up -d

docker-down:                              ## Stop playground stack
	docker compose down -v

# ── Cleanup ──────────────────────────────────────────────
clean:                                    ## Remove build artifacts
	rm -rf bin/ dist/

# ── Docs ─────────────────────────────────────────────────
docs-dev:                                 ## Run docs site dev server (website/)
	cd website && pnpm dev

docs-build:                               ## Build static docs site to website/out
	cd website && pnpm build
