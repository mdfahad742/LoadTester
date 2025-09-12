FROM golang:1.23.6 AS builder

WORKDIR /app
COPY . .
RUN go mod init loadtester || true
RUN go mod tidy
RUN go build -o loadtester main.go

# Use bookworm-slim instead of bullseye-slim
FROM debian:bookworm-slim
WORKDIR /app
COPY --from=builder /app/loadtester /app/loadtester

ENV URL="https://1.1.1.1"
ENV REQUESTS=100
ENV INTERVAL=10
ENV REPEAT_DELAY=5
ENV REPEAT_COUNT=3

ENTRYPOINT ["/app/loadtester"]
