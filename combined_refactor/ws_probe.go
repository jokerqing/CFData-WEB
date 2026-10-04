package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	defaultWSProbeAttempts  = 3
	defaultWSProbeTimeoutMS = 8000
	defaultWSProbeWorkers   = 20
	maxWSProbeAttempts      = 10
	maxWSProbeTimeoutMS     = 60000
	maxWSProbeWorkers       = 200
)

type wsProbeConfig struct {
	Enabled      bool   `json:"enabled"`
	Host         string `json:"host"`
	Path         string `json:"path"`
	Attempts     int    `json:"attempts"`
	TimeoutMS    int    `json:"timeoutMs"`
	MaxLatencyMS int    `json:"maxLatencyMs,omitempty"`
	Workers      int    `json:"workers"`
}

type wsProbeOutcome struct {
	Healthy     bool
	Successes   int
	Attempts    int
	AvgDuration time.Duration
	LastError   string
}

func normalizeWSProbeConfig(cfg wsProbeConfig) (wsProbeConfig, error) {
	if !cfg.Enabled {
		return wsProbeConfig{}, nil
	}

	cfg.Host = strings.TrimSpace(cfg.Host)
	if cfg.Host == "" {
		return cfg, fmt.Errorf("WebSocket 探测已开启，但未填写真实 Host/SNI")
	}
	if strings.Contains(cfg.Host, "://") || strings.ContainsAny(cfg.Host, "/?#") {
		return cfg, fmt.Errorf("WebSocket Host/SNI 只能填写域名，不能包含协议、端口或路径")
	}
	parsedHost, err := url.Parse("https://" + cfg.Host)
	if err != nil || parsedHost.Hostname() == "" || parsedHost.Hostname() != cfg.Host {
		return cfg, fmt.Errorf("WebSocket Host/SNI 无效: %q", cfg.Host)
	}

	cfg.Path = strings.TrimSpace(cfg.Path)
	if cfg.Path == "" {
		cfg.Path = "/"
	}
	if strings.Contains(cfg.Path, "://") {
		return cfg, fmt.Errorf("WebSocket 路径不能是完整 URL: %q", cfg.Path)
	}
	if !strings.HasPrefix(cfg.Path, "/") {
		cfg.Path = "/" + cfg.Path
	}
	parsedPath, err := url.ParseRequestURI(cfg.Path)
	if err != nil || parsedPath.IsAbs() || parsedPath.Host != "" || parsedPath.Fragment != "" {
		return cfg, fmt.Errorf("WebSocket 路径无效: %q", cfg.Path)
	}

	if cfg.Attempts <= 0 {
		cfg.Attempts = defaultWSProbeAttempts
	}
	if cfg.Attempts > maxWSProbeAttempts {
		return cfg, fmt.Errorf("WebSocket 探测次数不能超过 %d", maxWSProbeAttempts)
	}
	if cfg.TimeoutMS <= 0 {
		cfg.TimeoutMS = defaultWSProbeTimeoutMS
	}
	if cfg.TimeoutMS > maxWSProbeTimeoutMS {
		return cfg, fmt.Errorf("WebSocket 单次超时不能超过 %d 毫秒", maxWSProbeTimeoutMS)
	}
	if cfg.MaxLatencyMS < 0 || cfg.MaxLatencyMS > maxWSProbeTimeoutMS {
		return cfg, fmt.Errorf("WebSocket 平均延迟上限必须在 0-%d 毫秒之间", maxWSProbeTimeoutMS)
	}
	if cfg.Workers <= 0 {
		cfg.Workers = defaultWSProbeWorkers
	}
	if cfg.Workers > maxWSProbeWorkers {
		return cfg, fmt.Errorf("WebSocket 探测并发不能超过 %d", maxWSProbeWorkers)
	}
	return cfg, nil
}

func probeWebSocketEndpoint(ctx context.Context, ip string, port int, cfg wsProbeConfig) wsProbeOutcome {
	tlsConfig := tlsConfigWithRootCAs(cfg.Host)
	return probeWebSocketEndpointWithTLSConfig(ctx, ip, port, cfg, tlsConfig)
}

func probeWebSocketEndpointWithTLSConfig(ctx context.Context, ip string, port int, cfg wsProbeConfig, tlsConfig *tls.Config) wsProbeOutcome {
	outcome := wsProbeOutcome{Attempts: cfg.Attempts}
	var totalDuration time.Duration
	for attempt := 0; attempt < cfg.Attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			outcome.LastError = "探测任务已终止"
			break
		}

		attemptCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutMS)*time.Millisecond)
		duration, err := probeWebSocketOnce(attemptCtx, ip, port, cfg, tlsConfig)
		cancel()
		if err != nil {
			outcome.LastError = err.Error()
			break
		}
		outcome.Successes++
		totalDuration += duration
	}
	if outcome.Successes > 0 {
		outcome.AvgDuration = totalDuration / time.Duration(outcome.Successes)
	}
	outcome.Healthy = outcome.Successes == outcome.Attempts
	if outcome.Healthy && cfg.MaxLatencyMS > 0 && outcome.AvgDuration > time.Duration(cfg.MaxLatencyMS)*time.Millisecond {
		outcome.Healthy = false
		outcome.LastError = fmt.Sprintf("WebSocket 平均延迟 %.2fms 超过 %dms", float64(outcome.AvgDuration)/float64(time.Millisecond), cfg.MaxLatencyMS)
	}
	if outcome.Healthy {
		outcome.LastError = ""
	}
	return outcome
}

func probeWebSocketOnce(ctx context.Context, ip string, port int, cfg wsProbeConfig, tlsConfig *tls.Config) (time.Duration, error) {
	if port <= 0 || port > 65535 {
		return 0, fmt.Errorf("无效端口: %d", port)
	}
	target := net.JoinHostPort(ip, strconv.Itoa(port))
	probeTLSConfig := tlsConfig.Clone()
	probeTLSConfig.ServerName = cfg.Host
	probeTLSConfig.NextProtos = []string{"http/1.1"}
	dialer := websocket.Dialer{
		HandshakeTimeout: time.Duration(cfg.TimeoutMS) * time.Millisecond,
		TLSClientConfig:  probeTLSConfig,
		NetDialContext: func(dialCtx context.Context, network, _ string) (net.Conn, error) {
			return dialContextWithTimeout(dialCtx, "tcp", target, time.Duration(cfg.TimeoutMS)*time.Millisecond)
		},
		EnableCompression: false,
	}

	requestURI, err := url.ParseRequestURI(cfg.Path)
	if err != nil {
		return 0, fmt.Errorf("WebSocket 路径无效: %w", err)
	}
	probeURL := (&url.URL{Scheme: "wss", Host: cfg.Host, Path: requestURI.Path, RawPath: requestURI.RawPath, RawQuery: requestURI.RawQuery}).String()
	header := http.Header{}
	header.Set("User-Agent", "CFData-WebSocket-Probe/1.0")
	start := time.Now()
	conn, resp, err := dialer.DialContext(ctx, probeURL, header)
	duration := time.Since(start)
	if err != nil {
		if resp != nil {
			return duration, fmt.Errorf("WebSocket 握手失败: HTTP %d", resp.StatusCode)
		}
		return duration, fmt.Errorf("WebSocket 握手失败: %w", err)
	}
	defer conn.Close()
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "probe complete"), time.Now().Add(time.Second))
	return duration, nil
}

func runNSBWebSocketProbes(ctx context.Context, results []iptestResult, cfg wsProbeConfig, onProgress func(completed, healthy int), onResult func(idx int, outcome wsProbeOutcome)) bool {
	if len(results) == 0 {
		return false
	}
	workers := cfg.Workers
	if workers > len(results) {
		workers = len(results)
	}
	if workers <= 0 {
		workers = 1
	}

	jobs := make(chan int)
	type probeDone struct {
		idx     int
		outcome wsProbeOutcome
	}
	done := make(chan probeDone, workers)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		safeGo("nsb-websocket-probe", nil, func() {
			defer wg.Done()
			for idx := range jobs {
				res := &results[idx]
				outcome := probeWebSocketEndpoint(ctx, res.ipAddr, res.port, cfg)
				res.wsTested = true
				res.wsHealthy = outcome.Healthy
				res.wsSuccesses = outcome.Successes
				res.wsAttempts = outcome.Attempts
				res.wsDuration = outcome.AvgDuration
				res.wsError = outcome.LastError
				select {
				case done <- probeDone{idx: idx, outcome: outcome}:
				case <-ctx.Done():
					return
				}
			}
		})
	}

	next := 0
	inFlight := 0
	completed := 0
	healthy := 0
	canceled := false
	for next < len(results) && inFlight < workers {
		select {
		case jobs <- next:
			next++
			inFlight++
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return true
		}
	}
	for inFlight > 0 {
		select {
		case <-ctx.Done():
			canceled = true
			next = len(results)
			close(jobs)
			wg.Wait()
			return true
		case item := <-done:
			inFlight--
			completed++
			if item.outcome.Healthy {
				healthy++
			}
			if onResult != nil {
				onResult(item.idx, item.outcome)
			}
			if onProgress != nil {
				onProgress(completed, healthy)
			}
			if next < len(results) {
				select {
				case jobs <- next:
					next++
					inFlight++
				case <-ctx.Done():
					canceled = true
					next = len(results)
				}
			}
		}
	}
	close(jobs)
	wg.Wait()
	return canceled
}

func filterWSHealthyResults(results []iptestResult) []iptestResult {
	filtered := make([]iptestResult, 0, len(results))
	for _, result := range results {
		if result.wsTested && result.wsHealthy {
			filtered = append(filtered, result)
		}
	}
	return filtered
}
