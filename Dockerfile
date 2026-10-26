

# Go build stage
FROM golang:1.27-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /app/bin/exemplar .

# Runtime stage
FROM alpine:3.24
RUN apk --no-cache add ca-certificates \
    && addgroup -S app && adduser -S -G app app \
    && mkdir -p /app/data && chown -R app:app /app
WORKDIR /app
COPY --from=builder /app/bin/exemplar .
COPY --from=builder /app/exemplar-config.toml .
USER app
VOLUME ["/app/data"]
EXPOSE 8080
ENTRYPOINT ["./exemplar"]
CMD ["serve"]
