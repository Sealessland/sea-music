package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type sample struct {
	Duration time.Duration
	Failed   bool
}

type result struct {
	Name       string  `json:"name"`
	Requests   int     `json:"requests"`
	Errors     int     `json:"errors"`
	Throughput float64 `json:"throughput_rps"`
	P50MS      float64 `json:"p50_ms"`
	P95MS      float64 `json:"p95_ms"`
	P99MS      float64 `json:"p99_ms"`
	ElapsedMS  int64   `json:"elapsed_ms"`
}

type report struct {
	GeneratedAt       time.Time `json:"generated_at"`
	BaseURL           string    `json:"base_url"`
	Concurrency       int       `json:"concurrency"`
	Scenarios         []result  `json:"scenarios"`
	BacklogRecoveryMS int64     `json:"backlog_recovery_ms"`
}

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "loadtest: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	baseURL := strings.TrimRight(os.Getenv("SEA_LOAD_BASE_URL"), "/")
	token := os.Getenv("SEA_LOAD_ACCESS_TOKEN")
	videoID := os.Getenv("SEA_LOAD_VIDEO_ID")
	if baseURL == "" || token == "" || videoID == "" {
		return errors.New("SEA_LOAD_BASE_URL, SEA_LOAD_ACCESS_TOKEN, and SEA_LOAD_VIDEO_ID are required")
	}
	concurrency := envPositiveInt("SEA_LOAD_CONCURRENCY", 16)
	requests := envPositiveInt("SEA_LOAD_REQUESTS", 500)
	client := &http.Client{Timeout: 5 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	detail := executeScenario(ctx, "video-detail-read", concurrency, requests, func(index int) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/videos/"+videoID, nil)
	}, client)
	likes := executeScenario(ctx, "burst-like-toggle", concurrency, requests, func(index int) (*http.Request, error) {
		method := http.MethodPut
		if index%2 == 1 {
			method = http.MethodDelete
		}
		request, err := http.NewRequestWithContext(ctx, method, baseURL+"/api/v1/videos/"+videoID+"/like", bytes.NewReader(nil))
		if err == nil {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		return request, err
	}, client)
	recoveryStarted := time.Now()
	for {
		pending, failed, err := readOutboxBacklog(ctx, client, baseURL)
		if err != nil {
			return err
		}
		if failed > 0 {
			return fmt.Errorf("outbox backlog contains failed events: failed=%d", failed)
		}
		if pending == 0 {
			break
		}
		if time.Since(recoveryStarted) > 30*time.Second {
			return fmt.Errorf("outbox backlog did not recover within 30s: pending=%d", pending)
		}
		time.Sleep(50 * time.Millisecond)
	}
	output := report{
		GeneratedAt: time.Now().UTC(), BaseURL: baseURL, Concurrency: concurrency,
		Scenarios: []result{detail, likes}, BacklogRecoveryMS: time.Since(recoveryStarted).Milliseconds(),
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

func executeScenario(ctx context.Context, name string, concurrency, total int, factory func(int) (*http.Request, error), client *http.Client) result {
	started := time.Now()
	jobs := make(chan int)
	samples := make(chan sample, total)
	var wait sync.WaitGroup
	for range concurrency {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := range jobs {
				request, err := factory(index)
				if err != nil {
					samples <- sample{Failed: true}
					continue
				}
				requestStarted := time.Now()
				response, err := client.Do(request)
				elapsed := time.Since(requestStarted)
				failed := err != nil
				if response != nil {
					_, _ = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
					failed = failed || response.StatusCode < 200 || response.StatusCode >= 300
				}
				samples <- sample{Duration: elapsed, Failed: failed}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for index := range total {
			select {
			case <-ctx.Done():
				return
			case jobs <- index:
			}
		}
	}()
	wait.Wait()
	close(samples)
	durations := make([]time.Duration, 0, total)
	errorsCount := 0
	for item := range samples {
		durations = append(durations, item.Duration)
		if item.Failed {
			errorsCount++
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	elapsed := time.Since(started)
	return result{
		Name: name, Requests: len(durations), Errors: errorsCount,
		Throughput: float64(len(durations)) / elapsed.Seconds(),
		P50MS:      percentile(durations, 0.50), P95MS: percentile(durations, 0.95), P99MS: percentile(durations, 0.99),
		ElapsedMS: elapsed.Milliseconds(),
	}
}

func percentile(values []time.Duration, quantile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1) * quantile)
	return float64(values[index].Microseconds()) / 1000
}

func readOutboxBacklog(ctx context.Context, client *http.Client, baseURL string) (int64, int64, error) {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/metrics", nil)
	response, err := client.Do(request)
	if err != nil {
		return 0, 0, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, 0, err
	}
	return parseOutboxMetrics(data)
}

func parseOutboxMetrics(data []byte) (int64, int64, error) {
	values := make(map[string]int64, 3)
	for _, line := range strings.Split(string(data), "\n") {
		for _, state := range []string{"pending", "publishing", "failed"} {
			prefix := "sea_music_outbox_events{state=\"" + state + "\"} "
			if strings.HasPrefix(line, prefix) {
				value, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, prefix)), 10, 64)
				if err != nil {
					return 0, 0, fmt.Errorf("parse Outbox %s metric: %w", state, err)
				}
				values[state] = value
			}
		}
	}
	if len(values) != 3 {
		return 0, 0, errors.New("pending, publishing, and failed Outbox metrics are required")
	}
	return values["pending"] + values["publishing"], values["failed"], nil
}

func envPositiveInt(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
