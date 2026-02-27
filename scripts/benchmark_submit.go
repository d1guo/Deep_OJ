package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	targetURL    string
	concurrency  int
	totalReqs    int
	problemID    int
	timeLimitMs  int
	memoryLimit  int
	httpTimeout  int
	token        string
	username     string
	password     string
	email        string
	loginRetries int
)

func init() {
	flag.StringVar(&targetURL, "url", "http://127.0.0.1:18080/api/v1/submit", "目标 submit URL")
	flag.IntVar(&concurrency, "c", 50, "并发数")
	flag.IntVar(&totalReqs, "n", 5000, "总请求数")
	flag.IntVar(&problemID, "problem-id", 1, "题目 ID")
	flag.IntVar(&timeLimitMs, "time-limit-ms", 1000, "时间限制（毫秒）")
	flag.IntVar(&memoryLimit, "memory-limit-kb", 65536, "内存限制（KB）")
	flag.IntVar(&httpTimeout, "timeout-sec", 20, "HTTP 超时（秒）")
	flag.StringVar(&token, "token", strings.TrimSpace(os.Getenv("BENCH_TOKEN")), "JWT Token（可选；为空则自动登录获取）")
	flag.StringVar(&username, "username", getEnvDefault("BENCH_USERNAME", "admin"), "登录用户名")
	flag.StringVar(&password, "password", getEnvDefault("BENCH_PASSWORD", "password"), "登录密码")
	flag.StringVar(&email, "email", getEnvDefault("BENCH_EMAIL", "admin@example.com"), "注册邮箱")
	flag.IntVar(&loginRetries, "login-retries", 30, "登录重试次数（遇到 429 时有效）")
	flag.Parse()
}

type SubmitRequest struct {
	Code        string `json:"code"`
	Language    int    `json:"language"`
	TimeLimit   int    `json:"time_limit"`
	MemoryLimit int    `json:"memory_limit"`
	ProblemID   int    `json:"problem_id"`
}

type authResponse struct {
	Token string `json:"token"`
}

func main() {
	if concurrency <= 0 {
		fmt.Fprintln(os.Stderr, "并发数必须 > 0")
		os.Exit(2)
	}
	if totalReqs <= 0 {
		fmt.Fprintln(os.Stderr, "总请求数必须 > 0")
		os.Exit(2)
	}

	client := &http.Client{Timeout: time.Duration(httpTimeout) * time.Second}

	jwt, err := ensureToken(client)
	if err != nil {
		fmt.Fprintf(os.Stderr, "获取 JWT 失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("开始压测：请求数=%d，并发=%d\n", totalReqs, concurrency)
	fmt.Printf("目标 URL: %s\n", targetURL)
	fmt.Printf("鉴权模式: Bearer Token（长度=%d）\n", len(jwt))

	payload := SubmitRequest{
		Code:        "#include <iostream>\nint main(){ int a,b; std::cin >> a >> b; std::cout << a+b; return 0; }",
		Language:    1,
		TimeLimit:   timeLimitMs,
		MemoryLimit: memoryLimit,
		ProblemID:   problemID,
	}
	jsonData, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "序列化请求失败: %v\n", err)
		os.Exit(1)
	}

	latencies := make(chan time.Duration, totalReqs)
	errorsCh := make(chan string, totalReqs)

	var (
		wg          sync.WaitGroup
		sem         = make(chan struct{}, concurrency)
		statusMu    sync.Mutex
		statusCount = map[int]int{}
	)

	start := time.Now()
	for i := 0; i < totalReqs; i++ {
		wg.Add(1)
		sem <- struct{}{}

		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			t0 := time.Now()
			req, err := http.NewRequest(http.MethodPost, targetURL, bytes.NewReader(jsonData))
			if err != nil {
				errorsCh <- fmt.Sprintf("build_request_error: %v", err)
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+jwt)

			resp, err := client.Do(req)
			latency := time.Since(t0)
			if err != nil {
				errorsCh <- fmt.Sprintf("request_error: %v", err)
				return
			}
			defer resp.Body.Close()

			statusMu.Lock()
			statusCount[resp.StatusCode]++
			statusMu.Unlock()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
				errorsCh <- fmt.Sprintf("status_%d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
				return
			}

			_, _ = io.Copy(io.Discard, resp.Body)
			latencies <- latency
		}()
	}

	wg.Wait()
	close(latencies)
	close(errorsCh)
	totalTime := time.Since(start)

	var durations []float64
	for d := range latencies {
		durations = append(durations, float64(d.Microseconds())/1000.0)
	}
	sort.Float64s(durations)

	errSummary := map[string]int{}
	for e := range errorsCh {
		errSummary[e]++
	}

	successCount := len(durations)
	errCount := 0
	for _, c := range errSummary {
		errCount += c
	}

	fmt.Println("\n==================================")
	fmt.Printf("压测完成，总耗时：%v\n", totalTime)
	fmt.Printf("总请求数：%d\n", totalReqs)
	fmt.Printf("成功数：%d\n", successCount)
	fmt.Printf("失败数：%d\n", errCount)
	fmt.Printf("RPS：%.2f\n", float64(successCount)/totalTime.Seconds())

	fmt.Println("状态码分布：")
	codes := make([]int, 0, len(statusCount))
	for code := range statusCount {
		codes = append(codes, code)
	}
	sort.Ints(codes)
	for _, code := range codes {
		fmt.Printf("  %d -> %d\n", code, statusCount[code])
	}

	if errCount > 0 {
		fmt.Println("失败样本（聚合）：")
		type kv struct {
			k string
			v int
		}
		items := make([]kv, 0, len(errSummary))
		for k, v := range errSummary {
			items = append(items, kv{k: k, v: v})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].v > items[j].v })
		limit := 5
		if len(items) < limit {
			limit = len(items)
		}
		for i := 0; i < limit; i++ {
			fmt.Printf("  x%d  %s\n", items[i].v, items[i].k)
		}
	}
	fmt.Println("==================================")

	if successCount > 0 {
		fmt.Println("延迟分布（ms）：")
		fmt.Printf("最小值：%.2f\n", durations[0])
		fmt.Printf("P50：%.2f\n", durations[successCount*50/100])
		fmt.Printf("P90：%.2f\n", durations[successCount*90/100])
		fmt.Printf("P95：%.2f\n", durations[successCount*95/100])
		fmt.Printf("P99：%.2f\n", durations[successCount*99/100])
		fmt.Printf("最大值：%.2f\n", durations[successCount-1])
	}
}

func ensureToken(client *http.Client) (string, error) {
	if strings.TrimSpace(token) != "" {
		return strings.TrimSpace(token), nil
	}

	apiBase, err := deriveAPIBase(targetURL)
	if err != nil {
		return "", err
	}
	registerURL := strings.TrimRight(apiBase, "/") + "/api/v1/auth/register"
	loginURL := strings.TrimRight(apiBase, "/") + "/api/v1/auth/login"

	registerPayload := map[string]string{
		"username": username,
		"password": password,
		"email":    email,
	}
	registerStatus, registerBody, err := doJSON(client, http.MethodPost, registerURL, "", registerPayload)
	if err != nil {
		return "", fmt.Errorf("register request failed: %w", err)
	}
	if registerStatus != http.StatusCreated && registerStatus != http.StatusConflict && registerStatus != http.StatusTooManyRequests {
		return "", fmt.Errorf("register failed: status=%d body=%s", registerStatus, strings.TrimSpace(string(registerBody)))
	}

	loginPayload := map[string]string{
		"username": username,
		"password": password,
	}

	for i := 1; i <= loginRetries; i++ {
		status, body, reqErr := doJSON(client, http.MethodPost, loginURL, "", loginPayload)
		if reqErr != nil {
			if i == loginRetries {
				return "", fmt.Errorf("login request failed after retries: %w", reqErr)
			}
			time.Sleep(time.Second)
			continue
		}
		if status == http.StatusOK {
			var resp authResponse
			if err := json.Unmarshal(body, &resp); err != nil {
				return "", fmt.Errorf("parse login response failed: %w", err)
			}
			if strings.TrimSpace(resp.Token) == "" {
				return "", errors.New("login succeeded but token is empty")
			}
			return strings.TrimSpace(resp.Token), nil
		}
		if status == http.StatusTooManyRequests {
			if i == loginRetries {
				return "", fmt.Errorf("login rate limited after %d retries", loginRetries)
			}
			time.Sleep(time.Second)
			continue
		}
		return "", fmt.Errorf("login failed: status=%d body=%s", status, strings.TrimSpace(string(body)))
	}

	return "", errors.New("unable to get token")
}

func doJSON(client *http.Client, method, endpoint, jwt string, payload any) (int, []byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}

	req, err := http.NewRequest(method, endpoint, bytes.NewReader(data))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(jwt) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(jwt))
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, body, nil
}

func deriveAPIBase(submitURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(submitURL))
	if err != nil {
		return "", fmt.Errorf("invalid submit URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("submit URL missing scheme or host: %s", submitURL)
	}

	path := u.Path
	if strings.HasSuffix(path, "/api/v1/submit") {
		path = strings.TrimSuffix(path, "/api/v1/submit")
	} else {
		path = strings.TrimSuffix(path, "/")
	}

	u.Path = path
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func getEnvDefault(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}
