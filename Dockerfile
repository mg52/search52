############################
# 1) Build stage
############################
FROM golang:1.25.4-alpine AS builder

# Install git for module fetches
RUN apk add --no-cache git

WORKDIR /app

# Cache dependencies
COPY go.mod ./
RUN go mod download

# Copy service source and build statically
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /searchengine ./cmd/service

############################
# 2) Production stage
############################
FROM busybox:1.35

# Create a non-root user and prepare data directory
RUN adduser -D -u 1000 appuser \
 && mkdir /data \
 && chown appuser:appuser /data

# Copy the static binary
COPY --from=builder /searchengine /usr/local/bin/searchengine

# Switch to non-root
USER appuser

# Persist indexes in the mounted data volume by default
ENV SEARCH52_INDEX_DATA_DIR=/data

# Expose the API and admin UI ports
EXPOSE 8080 8081

# Declare a volume for persistent index data
VOLUME ["/data"]

# Run the binary
ENTRYPOINT ["/usr/local/bin/searchengine"]
