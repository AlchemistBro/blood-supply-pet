# syntax=docker/dockerfile:1.7
# Один Dockerfile на все сервисы: docker build -f deploy/docker/Dockerfile --build-arg SERVICE=audit .

ARG GO_VERSION=1.27.3
FROM golang:${GO_VERSION} AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG SERVICE
ARG VERSION=dev
RUN test -n "$SERVICE" || (echo "укажите --build-arg SERVICE=<имя сервиса>" && exit 1)
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath \
      -ldflags "-s -w -X github.com/AlchemistBro/blood-supply-pet/libs/runtime.Version=${VERSION}" \
      -o /out/app ./services/${SERVICE}/cmd/${SERVICE} && \
    go build -trimpath -ldflags "-s -w" -o /out/healthcheck ./tools/healthcheck

FROM gcr.io/distroless/static-debian12:nonroot
ARG SERVICE
ARG VERSION=dev
LABEL org.opencontainers.image.title="blood-supply-${SERVICE}" \
      org.opencontainers.image.source="https://github.com/AlchemistBro/blood-supply-pet" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/app /app
# В distroless нет shell и curl: healthcheck compose вызывает этот бинарник (tools/healthcheck).
COPY --from=build /out/healthcheck /healthcheck
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/app"]
