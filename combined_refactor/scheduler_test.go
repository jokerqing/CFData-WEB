package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLossRateWithinLimitZeroPercent(t *testing.T) {
	if !lossRateWithinLimit(0, 0) {
		t.Fatal("0% loss must qualify when maximum loss is 0%")
	}
	if lossRateWithinLimit(0.01, 0) {
		t.Fatal("1% loss must not qualify when maximum loss is 0%")
	}
}

func TestFilterOfficialCLIResultRowsRequiresLossAndSpeed(t *testing.T) {
	rows := []cliResultRow{
		{"ip": "1.1.1.1", "speed": "10.00MB/s", "lossRate": "0.00%"},
		{"ip": "2.2.2.2", "speed": "10.00MB/s", "lossRate": "1.00%"},
		{"ip": "3.3.3.3", "speed": "10.00MB/s"},
		{"ip": "4.4.4.4", "speed": "0.05MB/s", "lossRate": "0.00%"},
	}
	filtered := filterOfficialCLIResultRows(rows, true, true, 0.1, 0)
	if len(filtered) != 1 || filtered[0]["ip"] != "1.1.1.1" {
		t.Fatalf("expected only the zero-loss speed-qualified row, got %#v", filtered)
	}
}

func TestNormalizeScheduleDefaultsAndNextRun(t *testing.T) {
	cfg, err := normalizeScheduleConfig(scheduleConfig{Times: []string{"14:00", "02:00", "14:00"}, SpeedLimit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Times) != 2 || cfg.Times[0] != "02:00" || cfg.Times[1] != "14:00" {
		t.Fatalf("unexpected normalized times: %#v", cfg.Times)
	}
	cfg.Enabled = true
	now := time.Date(2026, 9, 16, 3, 0, 0, 0, schedulerLocation())
	next := nextScheduledRun(cfg, now)
	if got := next.Format("2006-01-02 15:04"); got != "2026-09-16 14:00" {
		t.Fatalf("unexpected next run: %s", got)
	}
}

func TestNSBStartDefinesLossMaxBeforeUse(t *testing.T) {
	source, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "function startNSBTask()")
	if start < 0 {
		t.Fatal("startNSBTask function not found")
	}
	end := strings.Index(text[start:], "function escapeHTML(v)")
	if end < 0 {
		t.Fatal("startNSBTask end boundary not found")
	}
	body := text[start : start+end]
	declaration := strings.Index(body, "const lossMax =")
	use := strings.Index(body, "nsbLossMaxValue = lossMax")
	if declaration < 0 || use < 0 || declaration > use {
		t.Fatal("startNSBTask must define lossMax before using it")
	}
}
