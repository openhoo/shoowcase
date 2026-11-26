VERSION ?= dev
GO ?= go
ENGINE ?= docker
IMAGE ?= shoowcase:$(VERSION)
VULN_TOOL := golang.org/x/vuln/cmd/govulncheck@v1.10.0

.PHONY: build test vet vuln smoke image release
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
