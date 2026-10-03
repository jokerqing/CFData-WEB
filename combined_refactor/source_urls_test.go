package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseNetworkSourceURLsIgnoresCommentsAndDuplicates(t *testing.T) {
	urls, err := parseNetworkSourceURLs("\n# disabled\n https://example.com/a.txt \nhttps://example.com/b.txt\nhttps://example.com/a.txt\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 2 || urls[0] != "https://example.com/a.txt" || urls[1] != "https://example.com/b.txt" {
		t.Fatalf("unexpected URLs: %#v", urls)
	}
}

func TestParseNetworkSourceURLsReportsLineNumber(t *testing.T) {
	_, err := parseNetworkSourceURLs("# ignored\nftp://example.com/list.txt")
	if err == nil || !strings.Contains(err.Error(), "第 2 行") {
		t.Fatalf("expected line-numbered validation error, got %v", err)
	}
}

func TestFetchNetworkSourceURLsRunsConcurrentlyAndKeepsOrder(t *testing.T) {
	var mu sync.Mutex
	started := 0
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		started++
		if started == 2 {
			close(release)
		}
		mu.Unlock()
		select {
		case <-release:
		case <-time.After(2 * time.Second):
			t.Error("URL requests did not overlap")
		}
		if r.URL.Path == "/a" {
			_, _ = w.Write([]byte("1.1.1.1 443\n"))
		} else {
			_, _ = w.Write([]byte("2.2.2.2 443\n"))
		}
	}))
	defer server.Close()

	content, name, err := fetchNetworkSourceURLs(context.Background(), server.URL+"/a\n# skip\n"+server.URL+"/b")
	if err != nil {
		t.Fatal(err)
	}
	if content != "1.1.1.1 443\n2.2.2.2 443" {
		t.Fatalf("unexpected combined content: %q", content)
	}
	if name != server.URL+"/a, "+server.URL+"/b" {
		t.Fatalf("unexpected source name: %q", name)
	}
}

func TestSourcePartialFailurePolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte("43.169.18.179:443\n"))
	}))
	defer server.Close()
	raw := server.URL + "/bad\n" + server.URL + "/good"
	if _, _, err := fetchNetworkSourceURLs(context.Background(), raw); err == nil {
		t.Fatal("strict policy must reject a failed source")
	}
	content, name, err := fetchNetworkSourceURLsWithPolicy(context.Background(), raw, true)
	if err != nil || content != "43.169.18.179:443" || name != server.URL+"/good" {
		t.Fatalf("partial source policy: content=%q name=%q error=%v", content, name, err)
	}
	if _, _, err := fetchNetworkSourceURLsWithPolicy(context.Background(), server.URL+"/bad", true); err == nil {
		t.Fatal("all failed sources must not succeed")
	}
}

func TestFilterNSBQualifiedRowsRequiresLossAndSpeed(t *testing.T) {
	rows := []cliResultRow{
		{"ip": "1.1.1.1", "speed": "10.00MB/s", "lossRate": "0.00%"},
		{"ip": "2.2.2.2", "speed": "10.00MB/s", "lossRate": "1.00%"},
		{"ip": "3.3.3.3", "speed": "0.05MB/s", "lossRate": "0.00%"},
	}
	filtered := filterCLIResultRowsByQualification(rows, true, true, 0.1, 0)
	if len(filtered) != 1 || filtered[0]["ip"] != "1.1.1.1" {
		t.Fatalf("expected only the zero-loss speed-qualified row, got %#v", filtered)
	}
}

func TestNSBSpeedWorkersDoNotCountLossyRowsAsQualified(t *testing.T) {
	results := []iptestResult{{lossRate: 0.01}, {lossRate: 0}}
	canceled := runNSBSpeedWorkers(context.Background(), results, 1, 1, 0.1, 0, nil, nil, func(idx int) (float64, string) {
		return 10 * 1024, ""
	})
	if canceled {
		t.Fatal("speed workers unexpectedly canceled")
	}
	if !results[0].speedTested || results[0].speedQualified {
		t.Fatalf("lossy row must be tested but unqualified: %#v", results[0])
	}
	if !results[1].speedTested || !results[1].speedQualified {
		t.Fatalf("zero-loss row must satisfy the target: %#v", results[1])
	}
}

func TestIndexContainsNSBScheduleAndMultilineURLInput(t *testing.T) {
	data, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{
		`<textarea id="nsbSourceUrl"`,
		`saveScheduleConfiguration(false, 'nsb')`,
		`id="nsbScheduleStatus"`,
		`value && !value.startsWith('#')`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("index.html missing %q", expected)
		}
	}
}
func TestNormalizeNSBSchedule(t *testing.T) {
	cfg, err := normalizeScheduleConfig(scheduleConfig{
		Mode: "nsb", Times: []string{"02:00", "14:00"}, Threads: 100,
		SpeedMin: 0.1, SpeedLimit: 5, LossMax: 0, SourceURLs: []string{
			"https://example.com/a.txt", "# disabled", "https://example.com/a.txt", "https://example.com/b.txt",
		},
		SpeedTest: 1, EnableTLS: true, ResultLimit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "nsb" || cfg.FallbackPort != 443 || len(cfg.SourceURLs) != 2 {
		t.Fatalf("unexpected normalized NSB schedule: %#v", cfg)
	}
	if defaultScheduleConfig().Enabled {
		t.Fatal("schedule must stay disabled until the user saves a valid configuration")
	}
}
