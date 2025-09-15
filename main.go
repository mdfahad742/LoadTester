package main

import (
	"compress/gzip"
	"crypto/tls"
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Config holds the load test configuration
type Config struct {
	URL         string
	Requests    int
	Concurrency int
	Interval    int
	RepeatCount int
	RepeatDelay int
	Burst       bool
	Compress    bool
	LogRequests bool
	MaxRetries  int
}

// Result stores metrics for each request
type Result struct {
	RequestID int
	Status    int
	Error     string
	Duration  time.Duration
	Retries   int
}

// getEnv reads env variable or returns default
func getEnv(key, def string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return def
}

// loadConfig reads environment variables into Config
func loadConfig() Config {
	reqs, _ := strconv.Atoi(getEnv("REQUESTS", "1000"))
	concurrency, _ := strconv.Atoi(getEnv("CONCURRENCY", "100"))
	interval, _ := strconv.Atoi(getEnv("INTERVAL", "5"))
	repeatCount, _ := strconv.Atoi(getEnv("REPEAT_COUNT", "1"))
	repeatDelay, _ := strconv.Atoi(getEnv("REPEAT_DELAY", "5"))
	maxRetries, _ := strconv.Atoi(getEnv("MAX_RETRIES", "2"))
	burst := getEnv("BURST", "false") == "true"
	compress := getEnv("COMPRESS", "false") == "true"
	logReq := getEnv("LOG_REQUESTS", "false") == "true"
	url := getEnv("URL", "https://www.google.com/generate_204")

	return Config{
		URL:         url,
		Requests:    reqs,
		Concurrency: concurrency,
		Interval:    interval,
		RepeatCount: repeatCount,
		RepeatDelay: repeatDelay,
		Burst:       burst,
		Compress:    compress,
		LogRequests: logReq,
		MaxRetries:  maxRetries,
	}
}

// createHTTPClient returns a high-performance HTTP client
func createHTTPClient() *http.Client {
	// Read VERIFY_TLS env var (default true)
	verifyTLS, _ := strconv.ParseBool(getEnv("VERIFY_TLS", "true"))

	tlsConfig := &tls.Config{
		InsecureSkipVerify: !verifyTLS, // skip verification if VERIFY_TLS=false
	}

	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:     tlsConfig,
			MaxIdleConns:        50_000,
			MaxIdleConnsPerHost: 50_000,
			DisableKeepAlives:   false,
		},
	}
}

// worker executes a single HTTP GET request with retries
func worker(client *http.Client, url string, id int, results chan<- Result, logReq bool, maxRetries int) {
	var r Result
	r.RequestID = id
	start := time.Now()
	var attempt int
	for attempt = 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequest("GET", url, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; LoadTester/1.0; +https://example.com)")
		if err != nil {
			r.Error = err.Error()
			break
		}

		resp, err := client.Do(req)
		duration := time.Since(start)
		r.Duration = duration
		r.Retries = attempt

		if err != nil {
			r.Error = err.Error()
			continue
		}

		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		r.Status = resp.StatusCode
		if resp.StatusCode >= 400 {
			r.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
			continue
		}
		r.Error = ""
		break
	}

	results <- r
}

// runLoad executes a single run of requests
func runLoad(cfg Config, run int, writer *csv.Writer, totalFailed *int64) time.Duration {
	fmt.Printf("Starting test run #%d\n", run)
	client := createHTTPClient()
	results := make(chan Result, cfg.Requests)
	var wg sync.WaitGroup
	startRun := time.Now()

	// Semaphore for concurrency control
	sem := make(chan struct{}, cfg.Concurrency)

	// Interval ticker for pacing requests if not burst
	var ticker *time.Ticker
	if !cfg.Burst && cfg.Interval > 0 {
		intervalPerReq := time.Duration(float64(cfg.Interval) / float64(cfg.Requests) * float64(time.Second))
		ticker = time.NewTicker(intervalPerReq)
		defer ticker.Stop()
	}

	send := func(id int) {
		defer wg.Done()
		worker(client, cfg.URL, id, results, cfg.LogRequests, cfg.MaxRetries)
		<-sem
	}

	for i := 1; i <= cfg.Requests; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go send(i)
		if !cfg.Burst && ticker != nil {
			<-ticker.C
		}
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	var success, fail int32
	var latencies []int64
	batch := make([][]string, 0, cfg.Requests)
	for r := range results {
		if r.Error != "" {
			fail++
		} else {
			success++
		}
		latencies = append(latencies, r.Duration.Milliseconds())
		batch = append(batch, []string{
			strconv.Itoa(run),
			strconv.Itoa(r.RequestID),
			strconv.Itoa(r.Status),
			r.Error,
			strconv.Itoa(int(r.Duration.Milliseconds())),
			strconv.Itoa(r.Retries),
		})
	}
	writer.WriteAll(batch)
	atomic.AddInt64(totalFailed, int64(fail))

	// Compute latency percentiles
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p50, p90, p99 := int64(0), int64(0), int64(0)
	if len(latencies) > 0 {
		p50 = latencies[len(latencies)/2]
		p90 = latencies[int(float64(len(latencies))*0.9)]
		p99 = latencies[int(float64(len(latencies))*0.99)]
	}

	durationRun := time.Since(startRun)
	fmt.Printf("Run %d completed: Requests=%d, Success=%d, Failed=%d, Time=%.2fs\n",
		run, cfg.Requests, success, fail, durationRun.Seconds())
	fmt.Printf("Latency(ms): p50=%d, p90=%d, p99=%d\n", p50, p90, p99)

	return durationRun
}

func main() {
	cfg := loadConfig()
	reportDir := getEnv("REPORT_DIR", "reports")
	logDir := getEnv("LOG_DIR", "logs")

	os.MkdirAll(reportDir, 0755)
	if cfg.LogRequests {
		os.MkdirAll(logDir, 0755)
		logFile, _ := os.Create(fmt.Sprintf("%s/results_%d.log", logDir, time.Now().Unix()))
		defer logFile.Close()
		log.SetOutput(logFile)
	}

	timestamp := time.Now().Format("20060102_150405")
	fileName := fmt.Sprintf("%s/results_%s.csv", reportDir, timestamp)
	var file *os.File
	var writer *csv.Writer

	if cfg.Compress {
		file, _ = os.Create(fileName + ".gz")
		defer file.Close()
		gzipWriter := gzip.NewWriter(file)
		defer gzipWriter.Close()
		writer = csv.NewWriter(gzipWriter)
		defer writer.Flush()
	} else {
		file, _ = os.Create(fileName)
		defer file.Close()
		writer = csv.NewWriter(file)
		defer writer.Flush()
	}

	writer.Write([]string{"RunID", "RequestID", "Status", "Error", "Duration(ms)", "Retries"})

	var totalFailed int64
	var totalDuration time.Duration
	for run := 1; run <= cfg.RepeatCount; run++ {
		duration := runLoad(cfg, run, writer, &totalFailed)
		totalDuration += duration
		if run < cfg.RepeatCount {
			fmt.Printf("Waiting %d seconds before next run...\n", cfg.RepeatDelay)
			time.Sleep(time.Duration(cfg.RepeatDelay) * time.Second)
		}
	}

	fmt.Printf("All test runs completed. Total failed requests: %d\n", totalFailed)
	fmt.Printf("Total wall-clock time for all runs: %.2fs\n", totalDuration.Seconds())
	fmt.Printf("Report saved to: %s\n", fileName)
}
