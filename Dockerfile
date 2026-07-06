############################
# 1) Build stage
############################
FROM golang:1.26.4-alpine AS builder

# Install git for module fetches and ca-certificates for the AI embedding
# client's outbound HTTPS calls (busybox ships neither, and has no package
# manager to add them later, so the bundle is copied from here below)
RUN apk add --no-cache git ca-certificates

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

# Copy CA certificates so outbound HTTPS to an embedding endpoint (OpenAI or
# any OpenAI-compatible API) verifies correctly. Only needed if AI-enabled
# indexes are used; harmless otherwise.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# Switch to non-root
USER appuser

# Persist indexes in the mounted data volume by default
ENV SEARCH52_INDEX_DATA_DIR=/data

# AI categorization is opt-in per index ("aiEnabled": true on /create-index)
# and requires an embedding endpoint. Set these at `docker run` time (like
# ADMINKEY below) rather than baking them into the image:
#   -e SEARCH52_EMBEDDING_BASE_URL=https://api.openai.com/v1
#   -e SEARCH52_EMBEDDING_MODEL=text-embedding-3-small
#   -e SEARCH52_EMBEDDING_API_KEY=sk-...   # optional, e.g. not needed for local Ollama

# Expose the API and admin UI ports
EXPOSE 8080 8081

# Declare a volume for persistent index data
VOLUME ["/data"]

# Run the binary
ENTRYPOINT ["/usr/local/bin/searchengine"]
