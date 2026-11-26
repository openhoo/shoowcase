FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.4-bookworm@sha256:ce59b41415e2524ae99926fe1f37ce48ba8c194dcbd2a56a65436cdcefa2bd34 AS builder
WORKDIR /src
ENV GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd/ ./cmd/
COPY internal/ ./internal/
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -mod=readonly -buildvcs=false -trimpath -ldflags="-s -w -buildid= -X main.version=${VERSION}" -o /out/shoowcase ./cmd/shoowcase

FROM scratch
ARG VERSION=dev
LABEL org.opencontainers.image.title="Shoowcase" \
      org.opencontainers.image.description="Workspace-scoped static hosting for MCP agents" \
      org.opencontainers.image.source="https://github.com/openhoo/shoowcase" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/shoowcase /shoowcase
COPY LICENSE NOTICE /usr/share/licenses/shoowcase/
USER 65532:65532
ENV SHOOWCASE_LISTEN=:7878
EXPOSE 7878
ENTRYPOINT ["/shoowcase"]
