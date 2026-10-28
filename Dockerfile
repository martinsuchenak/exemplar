# exemplar server image. See docs/deployment.md.
#
#   docker build --build-arg VERSION=$(git describe --tags --always) -t exemplar .

# Go build stage
FROM golang:1.27-alpine AS builder
ARG VERSION=dev
ARG DATE=unknown
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X github.com/martinsuchenak/exemplar/build.Version=${VERSION} -X github.com/martinsuchenak/exemplar/build.Date=${DATE}" \
    -o /app/bin/exemplar .

# Runtime stage
FROM alpine:3.24
# A fixed UID, so a host directory mounted at /app/data can be given to it
# (chown 10001:10001).
RUN apk --no-cache add ca-certificates \
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
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/health || exit 1
ENTRYPOINT ["exemplar"]
CMD ["serve"]
