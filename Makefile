# Local entry points for everything CI does. `make help` lists the targets.
VERSION      ?= $(or $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//'),0.0.0)
ARCHES       ?= amd64 arm64
NFPM_VERSION ?= v2.47.0
NFPM         ?= nfpm
SHA256       := $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo shasum -a 256)
LDFLAGS      := -s -w -X main.version=$(VERSION)

.PHONY: help ci fmt-check vet test build packages checksums release tools clean

help: ## show this help
	@grep -hE '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'

ci: fmt-check vet test build ## everything the CI workflow runs

fmt-check: ## fail if any file is not gofmt'ed
	@out=$$(gofmt -l .); [ -z "$$out" ] || { echo "gofmt needed:"; echo "$$out"; exit 1; }

vet: ## go vet
	go vet ./...

test: ## go test -race
	go test -race ./...

build: ## static linux binaries (amd64, arm64) -> dist/
	@mkdir -p dist
	@for arch in $(ARCHES); do \
		echo "build linux/$$arch"; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/cdtguard-$$arch ./cmd/cdtguard || exit 1; \
	done

packages: build ## .deb and .apk (needs nfpm: make tools)
	@command -v $(NFPM) >/dev/null || { echo "nfpm not found; run: make tools"; exit 1; }
	@mkdir -p build
	@for arch in $(ARCHES); do \
		cp dist/cdtguard-$$arch build/cdtguard; \
		for fmt in deb apk; do \
			GOARCH=$$arch VERSION=$(VERSION) $(NFPM) package -f packaging/nfpm.yaml -p $$fmt -t dist/ || exit 1; \
		done; \
	done

checksums: ## dist/SHA256SUMS
	cd dist && $(SHA256) *.deb *.apk cdtguard-* > SHA256SUMS

release: ci packages checksums ## what the release workflow runs (VERSION=x.y.z)

tools: ## install nfpm
	go install github.com/goreleaser/nfpm/v2/cmd/nfpm@$(NFPM_VERSION)

clean: ## remove build output
	rm -rf dist build
