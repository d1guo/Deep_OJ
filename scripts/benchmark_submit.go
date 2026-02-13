package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Config
var (
	targetURL   string
	concurrency int
	totalReqs   int
)

func init() {
	flag.StringVar(&targetURL, "url", "http://localhost:8080/api/v1/submit", "Target URL")
	flag.IntVar(&concurrency, "c", 50, "Concurrency level")
	flag.IntVar(&totalReqs, "n", 5000, "Total requests")
	flag.Parse()
}

type SubmitRequest struct {
	Code        string `json:"code"`
	Language    int    `json:"language"`
	TimeLimit   int    `json:"time_limit"`
	MemoryLimit int    `json:"memory_limit"`
	ProblemID   int    `json:"problem_id"`
}

func main() {
	fmt.Printf("🔥 Starting Benchmark: %d requests, %d concurrency\n", totalReqs, concurrency)
	fmt.Printf("   URL: %s\n", targetURL)

	// Prepare data
	payload := SubmitRequest{
		Code:        "#include <iostream>\nint main(){ int a,b; std::cin >> a >> b; std::cout << a+b; return 0; }",
		Language:    1, // C++
		TimeLimit:   1000,
		MemoryLimit: 65536,
		ProblemID:   1,
	}
	jsonData, _ := json.Marshal(payload)

	// Results channel
	latencies := make(chan time.Duration, totalReqs)
	errors := make(chan error, totalReqs)

	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency) // Semaphore for concurrency control

	start := time.Now()

	for i := 0; i < totalReqs; i++ {
		wg.Add(1)
		sem <- struct{}{} // Acquire

		go func(id int) {
			defer wg.Done()
			defer func() { <-sem }() // Release

			// Request
			t0 := time.Now()
			resp, err := http.Post(targetURL, "application/json", bytes.NewBuffer(jsonData))
			latency := time.Since(t0)

			if err != nil {
				errors <- err
				return
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body) // Read body to reuse connection

			if resp.StatusCode != 200 {
				errors <- fmt.Errorf("status %d", resp.StatusCode)
				return
			}

			latencies <- latency
		}(i)
	}

	wg.Wait()
	close(latencies)
	close(errors)
	totalTime := time.Since(start)

	// Process stats
	var durations []float64
	for d := range latencies {
		durations = append(durations, float64(d.Microseconds())/1000.0) // ms
	}
	sort.Float64s(durations)

	errCount := len(errors)
	successCount := len(durations)

	fmt.Println("\n==================================")
	fmt.Printf("✅ Benchmark Completed in %v\n", totalTime)
	fmt.Printf("   Total Requests: %d\n", totalReqs)
	fmt.Printf("   Success: %d\n", successCount)
	fmt.Printf("   Errors:  %d\n", errCount)
	fmt.Printf("   RPS:     %.2f\n", float64(successCount)/totalTime.Seconds())
	fmt.Println("==================================")

	if successCount > 0 {
		fmt.Printf("📊 Latency Distribution (ms):\n")
		fmt.Printf("   Min: %.2f\n", durations[0])
		fmt.Printf("   P50: %.2f\n", durations[successCount*50/100])
		fmt.Printf("   P90: %.2f\n", durations[successCount*90/100])
		fmt.Printf("   P95: %.2f\n", durations[successCount*95/100])
		fmt.Printf("   P99: %.2f\n", durations[successCount*99/100])
		fmt.Printf("   Max: %.2f\n", durations[successCount-1])
	}
}
