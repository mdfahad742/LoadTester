package main

import (
	"compress/gzip"
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
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
	Concurrency int
	Burst       bool
	Compress    bool
	LogRequests bool
	MaxRetries  int
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
	concurrency, _ := strconv.Atoi(getEnv("CONCURRENCY", "100"))
	burst := getEnv("BURST", "false") == "true"
	compress := getEnv("COMPRESS", "false") == "true"
	logReq := getEnv("LOG_REQUESTS", "true") == "true"
	maxRetries, _ := strconv.Atoi(getEnv("MAX_RETRIES", "1"))

	return Config{
		URL:         url,
		Requests:    reqs,
		Interval:    interval,
		RepeatDelay: repeatDelay,
		RepeatCount: repeatCount,
		Concurrency: concurrency,
		Burst:       burst,
		Compress:    compress,
		LogRequests: logReq,
		MaxRetries:  maxRetries,
	}
}

func createHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        1000,
			MaxIdleConnsPerHost: 1000,
			DisableKeepAlives:   false,
		},
	}
}

func worker(id int, cfg Config, client *http.Client, jobs <-chan int, results chan<- Result, wg *sync.WaitGroup) {
	defer wg.Done()
	for reqID := range jobs {
		var r Result
		start := time.Now()
		for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
			req, err := http.NewRequest("GET", cfg.URL, nil)
			if err != nil {
				r = Result{RequestID: reqID, Status: 0, Error: err.Error(), Duration: time.Since(start)}
				continue
			}

			if cfg.LogRequests {
				dump, _ := httputil.DumpRequestOut(req, true)
				log.Printf("[Request %d]\n%s\n", reqID, string(dump))
			}

			resp, err := client.Do(req)
			duration := time.Since(start)
			if err != nil {
				r = Result{RequestID: reqID, Status: 0, Error: err.Error(), Duration: duration}
				continue
			}
			defer resp.Body.Close()

			if cfg.LogRequests {
				dump, _ := httputil.DumpResponse(resp, true)
				log.Printf("[Response %d]\n%s\n", reqID, string(dump))
			}

			body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
			status := resp.StatusCode
			errStr := ""
			if status >= 400 {
				errStr = fmt.Sprintf("HTTP %d: %s", status, string(body))
			}

			r = Result{RequestID: reqID, Status: status, Error: errStr, Duration: duration}

			// Retry only if failed
			if r.Error == "" {
				break
			}
		}

		results <- r

		// Spread requests if not burst mode
		if !cfg.Burst && cfg.Interval > 0 {
			time.Sleep(time.Duration(float64(cfg.Interval)/float64(cfg.Requests)*1000) * time.Millisecond)
		}
	}
}

func runLoad(cfg Config, run int, writer *csv.Writer, totalFailed, totalSucceeded *int64, allLatencies *[]int64, mu *sync.Mutex) time.Duration {
	fmt.Printf("Starting test run #%d\n", run)
	client := createHTTPClient()
	jobs := make(chan int, cfg.Requests)
	results := make(chan Result, cfg.Requests)

	var wg sync.WaitGroup
	for w := 0; w < cfg.Concurrency; w++ {
		wg.Add(1)
		go worker(w, cfg, client, jobs, results, &wg)
	}

	go func() {
		for i := 1; i <= cfg.Requests; i++ {
			jobs <- i
		}
		close(jobs)
	}()

	var collectorWg sync.WaitGroup
	collectorWg.Add(1)
	startRun := time.Now()
	go func() {
		defer collectorWg.Done()
		for r := range results {
			writer.Write([]string{
				strconv.Itoa(run),
				strconv.Itoa(r.RequestID),
				strconv.Itoa(r.Status),
				r.Error,
				fmt.Sprintf("%d", r.Duration.Milliseconds()),
			})
			writer.Flush()

			if r.Error != "" {
				atomic.AddInt64(totalFailed, 1)
			} else {
				atomic.AddInt64(totalSucceeded, 1)
			}

			mu.Lock()
			*allLatencies = append(*allLatencies, r.Duration.Milliseconds())
			mu.Unlock()
		}
	}()

	wg.Wait()
	close(results)
	collectorWg.Wait()

	// Calculate per-run latency
	sorted := make([]int64, len(*allLatencies))
	copy(sorted, *allLatencies)
	if len(sorted) > 0 {
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		p50 := sorted[len(sorted)/2]
		p90 := sorted[int(float64(len(sorted))*0.9)]
		p99 := sorted[int(float64(len(sorted))*0.99)]
		fmt.Printf("Run %d completed: Requests=%d, Success=%d, Failed=%d, Time=%.2fs\n",
			run, cfg.Requests, *totalSucceeded, *totalFailed, time.Since(startRun).Seconds())
		fmt.Printf("Latency(ms): p50=%d, p90=%d, p99=%d\n", p50, p90, p99)
	}

	return time.Since(startRun)
}

func main() {
	cfg := loadConfig()

	reportDir := getEnv("REPORT_DIR", "reports")
	logDir := getEnv("LOG_DIR", "logs")

	os.MkdirAll(reportDir, 0755)
	if cfg.LogRequests {
		os.MkdirAll(logDir, 0755)
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

	if cfg.LogRequests {
		logFile, err := os.Create(fmt.Sprintf("%s/results_%s.log", logDir, timestamp))
		if err != nil {
			log.Fatalf("Failed to create log file: %v", err)
		}
		defer logFile.Close()
		log.SetOutput(logFile)
	}

	var totalFailed int64
	var totalSucceeded int64
	var allLatencies []int64
	var mu sync.Mutex
	var totalDuration time.Duration

	for run := 1; run <= cfg.RepeatCount; run++ {
		duration := runLoad(cfg, run, writer, &totalFailed, &totalSucceeded, &allLatencies, &mu)
		totalDuration += duration
		if run < cfg.RepeatCount {
			fmt.Printf("Waiting %d seconds before next run...\n", cfg.RepeatDelay)
			time.Sleep(time.Duration(cfg.RepeatDelay) * time.Second)
		}
	}

	totalRequests := int64(cfg.Requests) * int64(cfg.RepeatCount)
	sort.Slice(allLatencies, func(i, j int) bool { return allLatencies[i] < allLatencies[j] })
	p50, p90, p99 := int64(0), int64(0), int64(0)
	if len(allLatencies) > 0 {
		p50 = allLatencies[len(allLatencies)/2]
		p90 = allLatencies[int(float64(len(allLatencies))*0.9)]
		p99 = allLatencies[int(float64(len(allLatencies))*0.99)]
	}

	fmt.Println("--------------------------------------------------")
	fmt.Println("FINAL SUMMARY")
	fmt.Println("--------------------------------------------------")
	fmt.Printf("Total requests : %d\n", totalRequests)
	fmt.Printf("Succeeded      : %d\n", totalSucceeded)
	fmt.Printf("Failed         : %d\n", totalFailed)
	fmt.Printf("Overall Latency(ms): p50=%d, p90=%d, p99=%d\n", p50, p90, p99)
	fmt.Printf("Total wall-clock time: %.2fs\n", totalDuration.Seconds())
	fmt.Println("Report saved to file:", fileName)
	if cfg.Compress {
		fmt.Println("Compressed report:", fileName+".gz")
	}
	if cfg.LogRequests {
		fmt.Printf("Detailed logs saved to logs/results_%s.log\n", timestamp)
	}
}
