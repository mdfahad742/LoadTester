# ----------------------
# Build stage
# ----------------------
FROM golang:1.23.6 AS builder

WORKDIR /app

# Copy source code
COPY . .

# Init module if missing and tidy
RUN [ -f go.mod ] || go mod init loadtester
RUN go mod tidy

# Build binary
RUN go build -o loadtester main.go

# ----------------------
# Runtime stage
# ----------------------
FROM debian:bookworm-slim

WORKDIR /app

# Copy binary
COPY --from=builder /app/loadtester /app/loadtester

# Install certificates
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

# Create directories with proper permissions
RUN mkdir -p /app/reports /app/logs
RUN chmod 777 /app/reports /app/logs

# Default environment variables
ENV URL="https://1.1.1.1" \
    REQUESTS=100 \
    CONCURRENCY=10 \
    INTERVAL=5 \
    REPEAT_DELAY=5 \
    REPEAT_COUNT=3 \
    BURST=false \
    COMPRESS=false \
    LOG_REQUESTS=true \
    REPORT_DIR="/app/reports" \
    LOG_DIR="/app/logs"

# Entry point
ENTRYPOINT ["/app/loadtester"]
