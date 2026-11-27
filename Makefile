VERSION ?= dev
GO ?= go
PYTHON ?= python3
SQLC ?= sqlc
ENGINE ?= docker
IMAGE ?= shoowcase:$(VERSION)
VULN_TOOL := golang.org/x/vuln/cmd/govulncheck@v1.10.0

.PHONY: build test vet vuln smoke image release test-chart generate-sql
build:
	@mkdir -p bin
	CGO_ENABLED=0 $(GO) build -mod=readonly -buildvcs=false -trimpath -ldflags="-s -w -buildid= -X main.version=$(VERSION)" -o bin/shoowcase ./cmd/shoowcase
test:
	$(GO) test -race -count=1 ./...
vet:
	$(GO) vet ./...
vuln:
	$(GO) run $(VULN_TOOL) ./...
smoke: build
	python3 scripts/smoke.py --binary ./bin/shoowcase
image:
	$(ENGINE) build --build-arg VERSION=$(VERSION) -t $(IMAGE) .
release:
	./scripts/release.sh "$(VERSION)"

test-chart:
	$(PYTHON) scripts/test-helm.py

# Use the same generator as CI; building the server needs only the checked-in Go files.
generate-sql:
	@test "$$($(SQLC) version)" = "v1.31.1" || { echo 'Install sqlc v1.31.1 to regenerate queries.' >&2; exit 1; }
	$(SQLC) generate
