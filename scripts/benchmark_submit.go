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

// 参数配置
var (
	targetURL   string
	concurrency int
	totalReqs   int
)

func init() {
	flag.StringVar(&targetURL, "url", "http://localhost:8080/api/v1/submit", "目标 URL")
	flag.IntVar(&concurrency, "c", 50, "并发数")
	flag.IntVar(&totalReqs, "n", 5000, "总请求数")
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
	fmt.Printf("开始压测：请求数=%d，并发=%d\n", totalReqs, concurrency)
	fmt.Printf("目标 URL: %s\n", targetURL)

	// 准备请求数据
	payload := SubmitRequest{
		Code:        "#include <iostream>\nint main(){ int a,b; std::cin >> a >> b; std::cout << a+b; return 0; }",
		Language:    1, // C++
		TimeLimit:   1000,
		MemoryLimit: 65536,
		ProblemID:   1,
	}
	jsonData, _ := json.Marshal(payload)

	// 结果通道
	latencies := make(chan time.Duration, totalReqs)
	errors := make(chan error, totalReqs)

	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency) // 并发控制信号量

	start := time.Now()

	for i := 0; i < totalReqs; i++ {
		wg.Add(1)
			sem <- struct{}{} // 获取并发令牌

		go func(id int) {
			defer wg.Done()
				defer func() { <-sem }() // 释放并发令牌

				// 发起请求
				t0 := time.Now()
			resp, err := http.Post(targetURL, "application/json", bytes.NewBuffer(jsonData))
			latency := time.Since(t0)

			if err != nil {
				errors <- err
				return
			}
			defer resp.Body.Close()
				io.Copy(io.Discard, resp.Body) // 读取响应体，复用连接

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

	// 统计结果
	var durations []float64
	for d := range latencies {
		durations = append(durations, float64(d.Microseconds())/1000.0) // 毫秒
	}
	sort.Float64s(durations)

	errCount := len(errors)
	successCount := len(durations)

	fmt.Println("\n==================================")
	fmt.Printf("压测完成，总耗时：%v\n", totalTime)
	fmt.Printf("总请求数：%d\n", totalReqs)
	fmt.Printf("成功数：%d\n", successCount)
	fmt.Printf("失败数：%d\n", errCount)
	fmt.Printf("RPS：%.2f\n", float64(successCount)/totalTime.Seconds())
	fmt.Println("==================================")

	if successCount > 0 {
		fmt.Printf("延迟分布（ms）：\n")
		fmt.Printf("最小值：%.2f\n", durations[0])
		fmt.Printf("P50：%.2f\n", durations[successCount*50/100])
		fmt.Printf("P90：%.2f\n", durations[successCount*90/100])
		fmt.Printf("P95：%.2f\n", durations[successCount*95/100])
		fmt.Printf("P99：%.2f\n", durations[successCount*99/100])
		fmt.Printf("最大值：%.2f\n", durations[successCount-1])
	}
}
