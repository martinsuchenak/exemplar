# exemplar server image, built from source. Releases publish a multi-arch
# image built from the release binaries instead (Dockerfile.release,
# ghcr.io/martinsuchenak/exemplar). See docs/deployment.md.
#
#   task docker
#   docker build --build-arg VERSION=$(git describe --tags --always) --build-arg COMMIT=$(git rev-parse --short HEAD) -t exemplar .

# Build stage
FROM golang:1.27.3-alpine AS builder

ARG VERSION=dev
ARG COMMIT=unknown

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build \
    -ldflags "-s -w -X github.com/martinsuchenak/exemplar/build.Version=${VERSION} -X github.com/martinsuchenak/exemplar/build.Commit=${COMMIT} -X github.com/martinsuchenak/exemplar/build.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o /app/bin/exemplar .

# Runtime stage: keep in step with Dockerfile.release.
FROM alpine:3.24

# A fixed UID, so a host directory mounted at /app/data can be given to it
# (chown 10001:10001).
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 app && adduser -S -u 10001 -G app app \
    && mkdir -p /app/data && chown app:app /app/data

WORKDIR /app

# On the PATH, so `docker compose exec exemplar exemplar bootstrap` works.
COPY --from=builder /app/bin/exemplar /usr/local/bin/exemplar
COPY --from=builder /app/exemplar-config.toml .

USER app

# The database and blobs (database.path and storage.blob_dir are under data/).
VOLUME ["/app/data"]
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -q --spider http://127.0.0.1:8080/health || exit 1

ENTRYPOINT ["exemplar"]
CMD ["serve"]
