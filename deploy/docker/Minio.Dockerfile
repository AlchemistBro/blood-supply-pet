# Публичные готовые образы MinIO больше недоступны: собираем официальные версии из исходников.
ARG GO_VERSION=1.27.3
FROM golang:${GO_VERSION} AS build
ARG MINIO_VERSION=RELEASE.2025-10-15T17-29-55Z
ARG MC_VERSION=RELEASE.2025-08-13T08-35-41Z
ENV CGO_ENABLED=0 GOBIN=/out
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go install -trimpath -ldflags="-s -w" github.com/minio/minio@${MINIO_VERSION} && \
    go install -trimpath -ldflags="-s -w" github.com/minio/mc@${MC_VERSION}

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /out/minio /usr/bin/minio
COPY --from=build /out/mc /usr/bin/mc
USER 1000:1000
EXPOSE 9000 9001
ENTRYPOINT ["/usr/bin/minio"]
