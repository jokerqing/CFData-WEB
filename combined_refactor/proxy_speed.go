package main

// Optional deployment configuration enables production-equivalent downloads.
// Credentials stay in a private file and are passed to Xray on stdin only.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const googleProxySpeedURL = "https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb"
const proxySpeedBytes int64 = 50_000_000
const proxySpeedRounds = 3
const proxySpeedMinimumMbps = 20.0

type proxySpeedConfig struct {
	Enabled bool   `json:"enabled"`
	URI     string `json:"uri"`
}
type proxySpeedRound struct {
	Status  int     `json:"status"`
	Bytes   int64   `json:"bytes"`
	Seconds float64 `json:"seconds"`
	Mbps    float64 `json:"mbps"`
}
type proxySpeedProof struct {
	Endpoint              string            `json:"endpoint"`
	CheckedAt             int64             `json:"checkedAt"`
	MinimumMbps           float64           `json:"minimumMbps"`
	Rows                  []proxySpeedRound `json:"rows"`
	WSAverageMilliseconds float64           `json:"wsAverageMilliseconds,omitempty"`
	WSAttempts            int               `json:"wsAttempts,omitempty"`
}

var proxySpeedProofs sync.Map

func proxySpeedDir() string { p, _ := os.Executable(); return filepath.Dir(p) }
func loadProxySpeedConfig() (proxySpeedConfig, bool, error) {
	var cfg proxySpeedConfig
	data, err := os.ReadFile(filepath.Join(proxySpeedDir(), "cfdata-proxy-speed.json"))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, false, nil
	}
	if err != nil {
		return cfg, true, errors.New("真实代理测速配置无法读取")
	}
	if json.Unmarshal(data, &cfg) != nil {
		return cfg, true, errors.New("真实代理测速配置无效")
	}
	if !cfg.Enabled {
		return cfg, false, nil
	}
	u, err := url.Parse(cfg.URI)
	if err != nil || u.Scheme != "vless" || u.User == nil {
		return cfg, true, errors.New("真实代理测速参数无效")
	}
	q := u.Query()
	if q.Get("type") != "ws" || q.Get("security") != "tls" || q.Get("sni") == "" || q.Get("host") == "" || q.Get("path") == "" {
		return cfg, true, errors.New("真实代理测速传输配置无效")
	}
	return cfg, true, nil
}

func proxySpeedXrayConfig(cfg proxySpeedConfig, ip string, port, listenPort int) ([]byte, error) {
	u, err := url.Parse(cfg.URI)
	if err != nil {
		return nil, errors.New("代理参数无效")
	}
	q := u.Query()
	settings := map[string]any{
		"log":      map[string]any{"loglevel": "none"},
		"inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": listenPort, "protocol": "http", "settings": map[string]any{}}},
		"outbounds": []any{map[string]any{"protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": ip, "port": port, "users": []any{map[string]any{"id": u.User.Username(), "encryption": "none"}}}}},
			"streamSettings": map[string]any{"network": "ws", "security": "tls", "tlsSettings": map[string]any{"serverName": q.Get("sni"), "fingerprint": firstNonEmpty(q.Get("fp"), "chrome"), "alpn": strings.Split(firstNonEmpty(q.Get("alpn"), "http/1.1"), ",")}, "wsSettings": map[string]any{"path": q.Get("path"), "headers": map[string]any{"Host": q.Get("host")}}}}},
	}
	return json.Marshal(settings)
}

func downloadProxySpeedRound(ctx context.Context, client *http.Client, target string, minimumMbps ...float64) (proxySpeedRound, error) {
	row := proxySpeedRound{}
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return row, errors.New("测速地址无效")
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", proxySpeedBytes-1))
	req.Header.Set("Accept-Encoding", "identity")
	start := time.Now()
	if len(minimumMbps) > 0 && minimumMbps[0] > 0 {
		// With a fixed byte count, a round cannot recover to the minimum speed
		// after this elapsed time, even if the remaining bytes arrive instantly.
		budget := time.Duration(float64(proxySpeedBytes) * 8 / 1e6 / minimumMbps[0] * float64(time.Second))
		roundCtx, cancel := context.WithDeadline(ctx, start.Add(budget))
		defer cancel()
		req = req.WithContext(roundCtx)
	}
	response, err := client.Do(req)
	if err != nil {
		return row, fmt.Errorf("真实代理下载连接失败或超时: %v", err)
	}
	defer response.Body.Close()
	row.Status = response.StatusCode
	if response.StatusCode != http.StatusPartialContent || !strings.HasPrefix(response.Header.Get("Content-Range"), fmt.Sprintf("bytes 0-%d/", proxySpeedBytes-1)) {
		return row, errors.New("测速文件没有返回完整指定范围")
	}
	row.Bytes, err = io.Copy(io.Discard, io.LimitReader(response.Body, proxySpeedBytes+1))
	row.Seconds = time.Since(start).Seconds()
	if err != nil || row.Bytes != proxySpeedBytes {
		return row, errors.New("测速文件下载不完整")
	}
	row.Mbps = float64(row.Bytes) * 8 / 1e6 / row.Seconds
	return row, nil
}

func runProxySpeedTest(ctx context.Context, ip string, port int, target string) (float64, string, bool) {
	return runProxySpeedTestWithMinimum(ctx, ip, port, target, proxySpeedMinimumMbps)
}

func runProxySpeedTestWithMinimum(ctx context.Context, ip string, port int, target string, minimumMbps float64) (float64, string, bool) {
	minimumMbps = max(proxySpeedMinimumMbps, minimumMbps)
	cfg, enabled, err := loadProxySpeedConfig()
	if !enabled {
		return 0, "", false
	}
	if err != nil {
		return 0, err.Error(), true
	}
	target = firstNonEmpty(target, googleProxySpeedURL)
	if !strings.Contains(target, "://") {
		target = "https://" + target
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != "dl.google.com" || parsed.Path != "/linux/direct/google-chrome-stable_current_amd64.deb" {
		return 0, "真实代理测速需要使用配置的 Google 文件地址", true
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, "无法分配测速端口", true
	}
	listenPort := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	config, err := proxySpeedXrayConfig(cfg, ip, port, listenPort)
	if err != nil {
		return 0, "代理配置生成失败", true
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(childCtx, filepath.Join(proxySpeedDir(), "proxy-speed", "xray"), "run", "-c", "stdin:")
	command.Stdin = bytes.NewReader(config)
	command.Env = append(os.Environ(), "SSL_CERT_FILE="+filepath.Join(proxySpeedDir(), "proxy-speed", "ca-certificates.crt"))
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if command.Start() != nil {
		return 0, "真实代理测速组件无法启动", true
	}
	defer func() { cancel(); _ = command.Wait() }()
	proxyURL, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(listenPort))
	ready := false
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		conn, e := net.DialTimeout("tcp", proxyURL.Host, 100*time.Millisecond)
		if e == nil {
			conn.Close()
			ready = true
			break
		}
		select {
		case <-ctx.Done():
			return 0, "测速已取消", true
		case <-time.After(50 * time.Millisecond):
		}
	}
	if !ready {
		return 0, "真实代理测速组件未就绪", true
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: tlsConfigWithRootCAs(parsed.Hostname()), DisableCompression: true, DisableKeepAlives: true, TLSHandshakeTimeout: 6 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	proof := proxySpeedProof{Endpoint: net.JoinHostPort(ip, strconv.Itoa(port)), MinimumMbps: 1e9}
	for i := 0; i < proxySpeedRounds; i++ {
		row, e := downloadProxySpeedRound(ctx, client, target, minimumMbps)
		if e != nil {
			return 0, e.Error(), true
		}
		if row.Mbps < minimumMbps {
			return 0, fmt.Sprintf("真实代理下载低于 %.2f Mbps", minimumMbps), true
		}
		proof.Rows = append(proof.Rows, row)
		proof.MinimumMbps = min(proof.MinimumMbps, row.Mbps)
	}
	proof.CheckedAt = time.Now().Unix()
	proxySpeedProofs.Store(proof.Endpoint, proof)
	return proof.MinimumMbps * 1e6 / 8 / 1024 / 1024, "", true
}

// Only a completed scheduled run can replace this authoritative manifest.
// Intermediate scan files and old measurements cannot be consumed by HAProxy.
func publishProxySpeedManifest(cfg *cliConfig, rows []cliResultRow, started time.Time) error {
	_, enabled, err := loadProxySpeedConfig()
	if !enabled {
		return nil
	}
	if err != nil {
		return err
	}
	if !strings.HasPrefix(filepath.Base(cfg.outFile), "scheduled-nsb-results.") {
		return nil
	}
	proofs := []proxySpeedProof{}
	requiredMbps := max(proxySpeedMinimumMbps, cfg.nsbSpeedMin*8*1024*1024/1e6)
	for _, row := range rows {
		endpoint := net.JoinHostPort(row["ip"], row["port"])
		value, ok := proxySpeedProofs.Load(endpoint)
		if !ok {
			continue
		}
		proof := value.(proxySpeedProof)
		if proof.CheckedAt < started.Unix() || len(proof.Rows) != proxySpeedRounds || proof.MinimumMbps < requiredMbps {
			continue
		}
		if cfg.wsProbe.Enabled {
			latency, err := strconv.ParseFloat(strings.TrimSuffix(row["wsLatency"], "ms"), 64)
			if err != nil || latency <= 0 || row["wsSuccessRate"] != fmt.Sprintf("%d/%d", cfg.wsProbe.Attempts, cfg.wsProbe.Attempts) || (cfg.wsProbe.MaxLatencyMS > 0 && latency > float64(cfg.wsProbe.MaxLatencyMS)) {
				continue
			}
			proof.WSAverageMilliseconds = latency
			proof.WSAttempts = cfg.wsProbe.Attempts
		}
		proofs = append(proofs, proof)
	}
	minimumNodes := max(2, cfg.nsbSpeedLimit)
	if len(proofs) < minimumNodes {
		return fmt.Errorf("真实代理测速合格节点少于目标 %d 个，保留上次发布结果", minimumNodes)
	}
	data, err := json.MarshalIndent(map[string]any{"schema": 3, "producer": "CFData-WEB", "sourceIP": "192.168.88.19", "generatedAt": time.Now().Unix(), "speedURL": cfg.speedURL, "thresholdMbps": requiredMbps, "wsMaxLatencyMs": cfg.wsProbe.MaxLatencyMS, "bytesPerRound": proxySpeedBytes, "rounds": proxySpeedRounds, "nodes": proofs}, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(proxySpeedDir(), "cfdata-proxy-quality.json")
	tmp, err := os.CreateTemp(filepath.Dir(path), ".proxy-quality-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp.Name(), path)
}

// Count distinct public IPv4 addresses, rather than multiple ports of one IP.
func uniqueProxySpeedCandidates(results []iptestResult) []iptestResult {
	selected, _ := groupProxySpeedCandidates(results)
	return selected
}

// Keep eligible alternate ports for each IP: a working WS handshake does not
// guarantee that a particular port can carry a complete proxy download.
func groupProxySpeedCandidates(results []iptestResult) ([]iptestResult, map[string][]iptestResult) {
	groups := map[string][]iptestResult{}
	seenEndpoints := map[string]bool{}
	selected := make([]iptestResult, 0, len(results))
	for _, result := range results {
		ip := net.ParseIP(result.ipAddr)
		if ip == nil || ip.To4() == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() {
			continue
		}
		address := ip.String()
		endpoint := net.JoinHostPort(address, strconv.Itoa(result.port))
		if seenEndpoints[endpoint] {
			continue
		}
		seenEndpoints[endpoint] = true
		if len(groups[address]) == 0 {
			selected = append(selected, result)
		}
		groups[address] = append(groups[address], result)
	}
	return selected, groups
}

// The caller reports only the selected successful endpoint for each IP.
func runProxySpeedAlternatives(ctx context.Context, candidates []iptestResult, minimumMiBps float64, work func(iptestResult) (float64, string)) (iptestResult, float64, string) {
	var chosen iptestResult
	var speed float64
	var speedErr string
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return chosen, 0, "测速已取消"
		}
		chosen = candidate
		speed, speedErr = work(candidate)
		if speedErr == "" && speed/1024 >= minimumMiBps {
			return chosen, speed, ""
		}
	}
	return chosen, speed, speedErr
}
