# ----------------------
# Build stage
# ----------------------
FROM golang:1.23.6 AS builder

WORKDIR /app

# Copy source code
COPY . .

# Initialize Go module only if missing
RUN [ -f go.mod ] || go mod init loadtester
RUN go mod tidy

# Build Go binary
RUN go build -o loadtester main.go

# ----------------------
# Runtime stage
# ----------------------
FROM debian:bookworm-slim

WORKDIR /app

# Copy binary
COPY --from=builder /app/loadtester /app/loadtester

RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

# Create reports directory
RUN mkdir -p /app/reports

# Default environment variables
ENV URL="https://google.com" \
    REQUESTS=100 \
    INTERVAL=3 \
    REPEAT_DELAY=5 \
    REPEAT_COUNT=3 \
    REPORT_DIR="/app/reports"

# Run binary
ENTRYPOINT ["/app/loadtester"]
