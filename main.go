package main

import (
	"compress/gzip"
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

type Config struct {
	URL         string
	Requests    int
	Interval    int // seconds
	RepeatDelay int
	RepeatCount int
	Burst       bool
	Compress    bool
}

type Result struct {
	RequestID int
	Status    int
	Error     string
	Duration  time.Duration
}

func getEnv(key, def string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return def
}

func loadConfig() Config {
	url := getEnv("URL", "https://www.google.com/generate_204")
	reqs, _ := strconv.Atoi(getEnv("REQUESTS", "10"))
	interval, _ := strconv.Atoi(getEnv("INTERVAL", "10"))
	repeatDelay, _ := strconv.Atoi(getEnv("REPEAT_DELAY", "5"))
	repeatCount, _ := strconv.Atoi(getEnv("REPEAT_COUNT", "1"))
	burst := getEnv("BURST", "false") == "true"
	compress := getEnv("COMPRESS", "false") == "true"

	return Config{
		URL:         url,
		Requests:    reqs,
		Interval:    interval,
		RepeatDelay: repeatDelay,
		RepeatCount: repeatCount,
		Burst:       burst,
		Compress:    compress,
	}
}

func createHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100_000,
			MaxIdleConnsPerHost: 100_000,
			DisableKeepAlives:   false,
		},
	}
}

func worker(client *http.Client, url string, id int, results chan<- Result) {
	start := time.Now()
	resp, err := client.Get(url)
	duration := time.Since(start)
	if err != nil {
		results <- Result{RequestID: id, Status: 0, Error: err.Error(), Duration: duration}
		return
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	resp.Body.Close()
	status := resp.StatusCode
	errStr := ""
	if status >= 400 {
		errStr = fmt.Sprintf("HTTP %d: %s", status, string(body))
	}
	results <- Result{RequestID: id, Status: status, Error: errStr, Duration: duration}
}

func runLoad(cfg Config, run int, writer *csv.Writer, totalFailed *int64) time.Duration {
	fmt.Printf("Starting test run #%d\n", run)
	client := createHTTPClient()
	results := make(chan Result, cfg.Requests)
	var wg sync.WaitGroup

	startRun := time.Now()

	// Determine number of concurrent workers
	numGoroutines := cfg.Requests
	if !cfg.Burst && cfg.Requests > 1000 {
		numGoroutines = 1000
	}
	sem := make(chan struct{}, numGoroutines)

	var ticker *time.Ticker
	if !cfg.Burst {
		intervalPerReq := time.Duration(float64(cfg.Interval)/float64(cfg.Requests)*1e9) * time.Nanosecond
		ticker = time.NewTicker(intervalPerReq)
		defer ticker.Stop()
	}

	send := func(id int) {
		defer wg.Done()
		worker(client, cfg.URL, id, results)
		<-sem
	}

	for i := 1; i <= cfg.Requests; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go send(i)
		if !cfg.Burst {
			<-ticker.C
		}
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	batch := make([][]string, 0, cfg.Requests)
	var success, fail int32
	var latencies []int64
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
			fmt.Sprintf("%d", r.Duration.Milliseconds()),
		})
	}

	writer.WriteAll(batch)
	atomic.AddInt64(totalFailed, int64(fail))

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

	// Ensure reports dir exists
	reportDir := getEnv("REPORT_DIR", "/app/reports")
	if err := os.MkdirAll(reportDir, 0755); err != nil {
		log.Fatalf("Failed to create reports directory: %v", err)
	}

	timestamp := time.Now().Format("20060102_150405")
	fileName := fmt.Sprintf("%s/results_%s.csv", reportDir, timestamp)

	var file *os.File
	var writer *csv.Writer
	var err error

	if cfg.Compress {
		file, err = os.Create(fileName + ".gz")
		if err != nil {
			log.Fatalf("Failed to create file: %v", err)
		}
		defer file.Close()
		gzipWriter := gzip.NewWriter(file)
		defer gzipWriter.Close()
		writer = csv.NewWriter(gzipWriter)
		defer writer.Flush()
	} else {
		file, err = os.Create(fileName)
		if err != nil {
			log.Fatalf("Failed to create file: %v", err)
		}
		defer file.Close()
		writer = csv.NewWriter(file)
		defer writer.Flush()
	}

	writer.Write([]string{"RunID", "RequestID", "Status", "Error", "Duration(ms)"})

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
	fmt.Printf("Report saved to file: %s\n", fileName)
	if cfg.Compress {
		fmt.Printf("Compressed report: %s.gz\n", fileName)
	}
}
