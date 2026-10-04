package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProxySpeedDownloadEvidence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		bytes   int64
		rangeOK bool
		valid   bool
	}{
		{"complete", 206, proxySpeedBytes, true, true},
		{"truncated", 206, proxySpeedBytes - 1, true, false},
		{"oversized", 206, proxySpeedBytes + 1, true, false},
		{"wrong-status", 200, proxySpeedBytes, true, false},
		{"wrong-range", 206, proxySpeedBytes, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Range") != fmt.Sprintf("bytes=0-%d", proxySpeedBytes-1) {
					t.Error("range missing")
				}
				if tc.rangeOK {
					w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/999999999", proxySpeedBytes-1))
				}
				w.WriteHeader(tc.status)
				block := make([]byte, 100_000)
				remaining := tc.bytes
				for remaining > 0 {
					n := min(remaining, int64(len(block)))
					if _, err := w.Write(block[:n]); err != nil {
						return
					}
					remaining -= n
				}
			}))
			defer server.Close()
			row, err := downloadProxySpeedRound(context.Background(), server.Client(), server.URL)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if tc.valid && (row.Bytes != proxySpeedBytes || row.Mbps <= 0) {
				t.Fatal("invalid throughput evidence")
			}
		})
	}
}

func TestProxySpeedCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := downloadProxySpeedRound(ctx, http.DefaultClient, "https://dl.google.com/test"); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestProxySpeedAbortsRoundThatCannotReachRequiredThroughput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/999999999", proxySpeedBytes-1))
		w.WriteHeader(http.StatusPartialContent)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 15 * time.Second
	start := time.Now()
	// 50 MB at 400 Mbps allows one second including connection and transfer.
	_, err := downloadProxySpeedRound(context.Background(), client, server.URL, 400)
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("hopeless round did not stop at its throughput deadline: err=%v elapsed=%v", err, time.Since(start))
	}
}

func TestProxySpeedTriesAlternatePortAndKeepsItsWSEvidence(t *testing.T) {
	input := []iptestResult{{ipAddr: "216.236.59.147", port: 443}, {ipAddr: "216.236.59.147", port: 1891, wsDuration: 400 * time.Millisecond}, {ipAddr: "216.236.59.147", port: 1891}, {ipAddr: "43.174.218.1", port: 443}}
	selected, groups := groupProxySpeedCandidates(input)
	if len(selected) != 2 || len(groups["216.236.59.147"]) != 2 {
		t.Fatal("must retain alternate ports without counting the IP twice")
	}
	var attempted []int
	chosen, speed, speedErr := runProxySpeedAlternatives(context.Background(), groups["216.236.59.147"], 5, func(candidate iptestResult) (float64, string) {
		attempted = append(attempted, candidate.port)
		if candidate.port == 443 {
			return 0, "connection reset"
		}
		return 6 * 1024, ""
	})
	if fmt.Sprint(attempted) != "[443 1891]" || chosen.port != 1891 || chosen.wsDuration != 400*time.Millisecond || speed != 6*1024 || speedErr != "" {
		t.Fatalf("alternate endpoint evidence lost: %#v speed=%v err=%v attempted=%v", chosen, speed, speedErr, attempted)
	}
}

func TestProxyCandidatesCountDistinctPublicIPv4(t *testing.T) {
	results := []iptestResult{{ipAddr: "43.169.18.179", port: 443}, {ipAddr: "43.169.18.179", port: 8443}, {ipAddr: "38.55.199.128", port: 443}, {ipAddr: "127.0.0.1", port: 1234}, {ipAddr: "192.168.88.18", port: 443}, {ipAddr: "::1", port: 443}}
	filtered := uniqueProxySpeedCandidates(results)
	if len(filtered) != 2 || filtered[0].port != 443 {
		t.Fatalf("must count only distinct public IPv4: %#v", filtered)
	}
}

func TestProxySpeedUsesCandidateAndProductionTransport(t *testing.T) {
	cfg := proxySpeedConfig{Enabled: true, URI: "vless://test-user@192.168.88.18:443?security=tls&type=ws&sni=worker.example&host=worker.example&path=%2Fproxyip%3Drelay.example%3A443&fp=chrome&alpn=http%2F1.1"}
	data, err := proxySpeedXrayConfig(cfg, "43.174.218.1", 443, 12345)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if json.Unmarshal(data, &value) != nil {
		t.Fatal("invalid config")
	}
	for _, v := range []string{"43.174.218.1", "worker.example", "/proxyip=relay.example:443", "chrome", "127.0.0.1"} {
		if !strings.Contains(string(data), v) {
			t.Fatalf("missing %s", v)
		}
	}
	if strings.Contains(string(data), "192.168.88.18") {
		t.Fatal("measurement must dial candidate, not load balancer")
	}
}
