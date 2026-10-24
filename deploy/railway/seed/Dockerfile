FROM golang:1.27 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags "-s -w" -o /out/seed ./tools/seed

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/seed /seed
USER 65532:65532
ENTRYPOINT ["/seed"]
