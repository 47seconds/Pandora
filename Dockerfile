# Builder stage
FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o pandora .

# Final stage
FROM alpine:latest
RUN apk add --no-cache ca-certificates tzdata
RUN addgroup -S appgroup && adduser -S appuser -G appgroup && mkdir /data && chown appuser:appgroup /data
USER appuser
WORKDIR /app
COPY --from=builder /app/pandora .
# sources/ holds per-service test definitions used by later phases
COPY --from=builder /app/sources ./sources
# dashboard build files, served at /dashboard/ (PANDORA_STATIC_DIR)
COPY --from=builder /app/static ./static
ENV PANDORA_ADDR=:8847 PANDORA_DB_PATH=/data/pandora.db
VOLUME /data
EXPOSE 8847
CMD ["./pandora"]
