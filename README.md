# Go Load Tester

A lightweight and scalable HTTP load testing tool written in **Go**.  
It supports high concurrency with custom configuration options and can be containerized with Docker.

---

## 🚀 Features
- High-concurrency load testing using **goroutines**
- Configurable request count, concurrency, and duration
- Uses a custom **HTTP client** optimized for performance
- Dockerized for easy deployment and portability

---

## 📦 Installation

### Prerequisites
- [Go 1.23+](https://go.dev/dl/)
- [Docker](https://www.docker.com/)

### Clone the repository
```bash
git clone https://github.com/your-username/loadtester.git
cd loadtester
```

### Build binary
```bash
go build -o loadtester main.go
```

---

## 🐳 Docker Setup

### Build the Docker image
```bash
docker build -t loadtester .
```

### Run the container
```bash
docker run --rm loadtester
```

If you want to mount your local files (e.g., configs), run:
```bash
docker run --rm -v $(pwd):/app loadtester
```

---

## ⚡ Usage

Run the tool directly:
```bash
./loadtester -url https://example.com -concurrency 100 -requests 10000
```

Or inside Docker:
```bash
docker run --rm loadtester -url https://example.com -concurrency 100 -requests 10000
```

---

## 🔧 Configuration Options

| Flag           | Description                          | Default    |
|----------------|--------------------------------------|------------|
| `-url`         | Target URL for load testing          | (required) |
| `-concurrency` | Number of concurrent workers         | `50`       |
| `-requests`    | Total number of requests to send     | `1000`     |
| `-timeout`     | HTTP client timeout (seconds)        | `15`       |

---

## 📊 Example Output
```bash
$ ./loadtester -url https://example.com -concurrency 200 -requests 5000

[INFO] Starting load test: 5000 requests with 200 workers
[INFO] Completed in 12.3s
[STATS] Success: 4970 | Failed: 30 | RPS: 406
```
