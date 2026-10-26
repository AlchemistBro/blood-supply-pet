# syntax=docker/dockerfile:1.7
ARG GO_VERSION=1.27.3
FROM golang:${GO_VERSION} AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY libs ./libs
COPY tools/chaos/inject ./tools/chaos/inject
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /inject ./tools/chaos/inject
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /inject /inject
USER 65532:65532
ENTRYPOINT ["/inject"]
