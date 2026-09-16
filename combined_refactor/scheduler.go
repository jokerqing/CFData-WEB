package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const schedulerTimezone = "Asia/Shanghai"

type scheduleConfig struct {
	Enabled       bool     `json:"enabled"`
	Times         []string `json:"times"`
	Timezone      string   `json:"timezone"`
	IPType        int      `json:"ipType"`
	Threads       int      `json:"threads"`
	Port          int      `json:"port"`
	Delay         int      `json:"delay"`
	ScanMode      string   `json:"scanMode"`
	SpeedMin      float64  `json:"speedMin"`
	SpeedLimit    int      `json:"speedLimit"`
	LossMax       float64  `json:"lossMax"`
	SpeedURL      string   `json:"speedURL"`
	TargetDC      string   `json:"targetDC"`
	Format        string   `json:"format"`
	GitHubRepo    string   `json:"githubRepo"`
	GitHubBranch  string   `json:"githubBranch"`
	GitHubPath    string   `json:"githubPath"`
	GitHubMessage string   `json:"githubMessage"`
}

type scheduleSaveRequest struct {
	Config scheduleConfig `json:"config"`
	Token  string         `json:"token"`
}

type scheduleRuntime struct {
	mu              sync.Mutex
	config          scheduleConfig
	running         bool
	lastRun         time.Time
	lastSuccess     time.Time
	lastError       string
	lastTriggerKey  string
	tokenConfigured bool
}

var officialScheduler scheduleRuntime

func defaultScheduleConfig() scheduleConfig {
	return scheduleConfig{
		Enabled: true, Times: []string{"02:00", "14:00"}, Timezone: schedulerTimezone,
		IPType: 4, Threads: 100, Port: 443, Delay: 500, ScanMode: scanModeTCPing,
		SpeedMin: 0.1, SpeedLimit: 5, LossMax: 0, SpeedURL: autoSpeedURLValue,
		Format: "txt", GitHubBranch: "main", GitHubPath: "results/ip.txt",
		GitHubMessage: "scheduled cfdata qualified results",
	}
}

func schedulerBaseDir() string {
	executable, err := os.Executable()
	if err == nil && strings.TrimSpace(executable) != "" {
		return filepath.Dir(executable)
	}
	return filepath.Dir(os.Args[0])
}

func schedulerConfigPath() string { return filepath.Join(schedulerBaseDir(), "cfdata-schedule.json") }
func schedulerTokenPath() string  { return filepath.Join(schedulerBaseDir(), "cfdata-schedule.token") }

func normalizeScheduleConfig(cfg scheduleConfig) (scheduleConfig, error) {
	if len(cfg.Times) == 0 {
		cfg.Times = []string{"02:00", "14:00"}
	}
	seen := map[string]bool{}
	times := make([]string, 0, len(cfg.Times))
	for _, raw := range cfg.Times {
		value := strings.TrimSpace(raw)
		parsed, err := time.Parse("15:04", value)
		if err != nil {
			return cfg, fmt.Errorf("无效执行时间 %q，请使用 HH:MM", value)
		}
		value = parsed.Format("15:04")
		if !seen[value] {
			seen[value] = true
			times = append(times, value)
		}
	}
	sort.Strings(times)
	cfg.Times = times
	cfg.Timezone = schedulerTimezone
	if cfg.IPType != 4 && cfg.IPType != 6 {
		cfg.IPType = 4
	}
	if cfg.Threads <= 0 {
		cfg.Threads = 100
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		cfg.Port = 443
	}
	if cfg.Delay < 0 {
		cfg.Delay = 0
	}
	if cfg.ScanMode != scanModeHTTPing {
		cfg.ScanMode = scanModeTCPing
	}
	if cfg.SpeedMin <= 0 {
		cfg.SpeedMin = 0.1
	}
	if cfg.SpeedLimit <= 0 {
		return cfg, errors.New("定时测速结果数量必须大于 0")
	}
	if cfg.LossMax < 0 || cfg.LossMax > 100 {
		return cfg, errors.New("最大丢包率必须在 0-100 之间")
	}
	if strings.TrimSpace(cfg.SpeedURL) == "" {
		cfg.SpeedURL = autoSpeedURLValue
	}
	cfg.Format = strings.ToLower(strings.TrimSpace(cfg.Format))
	if cfg.Format != "csv" {
		cfg.Format = "txt"
	}
	cfg.GitHubRepo = strings.Trim(strings.TrimSpace(cfg.GitHubRepo), "/")
	cfg.GitHubBranch = strings.TrimSpace(cfg.GitHubBranch)
	if cfg.GitHubBranch == "" {
		cfg.GitHubBranch = "main"
	}
	cfg.GitHubPath = strings.Trim(strings.TrimSpace(cfg.GitHubPath), "/")
	if cfg.GitHubPath == "" {
		cfg.GitHubPath = "results/ip." + cfg.Format
	}
	if !strings.HasSuffix(strings.ToLower(cfg.GitHubPath), "."+cfg.Format) {
		cfg.GitHubPath = strings.TrimSuffix(cfg.GitHubPath, filepath.Ext(cfg.GitHubPath)) + "." + cfg.Format
	}
	if strings.TrimSpace(cfg.GitHubMessage) == "" {
		cfg.GitHubMessage = "scheduled cfdata qualified results"
	}
	return cfg, nil
}

func loadScheduleConfig() scheduleConfig {
	cfg := defaultScheduleConfig()
	data, err := os.ReadFile(schedulerConfigPath())
	if err == nil {
		if unmarshalErr := json.Unmarshal(data, &cfg); unmarshalErr != nil {
			fmt.Printf("[schedule] 配置解析失败: %v\n", unmarshalErr)
		}
	}
	normalized, err := normalizeScheduleConfig(cfg)
	if err != nil {
		fmt.Printf("[schedule] 配置无效，已停用: %v\n", err)
		cfg.Enabled = false
		return cfg
	}
	return normalized
}

func saveSchedule(req scheduleSaveRequest) error {
	cfg, err := normalizeScheduleConfig(req.Config)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(req.Token)
	existingToken, _ := os.ReadFile(schedulerTokenPath())
	if token == "" {
		token = strings.TrimSpace(string(existingToken))
	}
	parts := strings.Split(cfg.GitHubRepo, "/")
	if cfg.Enabled && (len(parts) != 2 || parts[0] == "" || parts[1] == "" || token == "") {
		return errors.New("启用定时任务需要有效的 GitHub 仓库和 Token")
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(schedulerConfigPath(), data, 0o600); err != nil {
		return err
	}
	if strings.TrimSpace(req.Token) != "" {
		if err := os.WriteFile(schedulerTokenPath(), []byte(strings.TrimSpace(req.Token)+"\n"), 0o600); err != nil {
			return err
		}
		_ = os.Chmod(schedulerTokenPath(), 0o600)
	}
	officialScheduler.mu.Lock()
	officialScheduler.config = cfg
	officialScheduler.tokenConfigured = token != ""
	officialScheduler.lastError = ""
	officialScheduler.mu.Unlock()
	return nil
}

func schedulerLocation() *time.Location {
	return time.FixedZone(schedulerTimezone, 8*60*60)
}

func nextScheduledRun(cfg scheduleConfig, now time.Time) time.Time {
	if !cfg.Enabled || len(cfg.Times) == 0 {
		return time.Time{}
	}
	localNow := now.In(schedulerLocation())
	var next time.Time
	for dayOffset := 0; dayOffset <= 1; dayOffset++ {
		day := localNow.AddDate(0, 0, dayOffset)
		for _, value := range cfg.Times {
			parsed, err := time.Parse("15:04", value)
			if err != nil {
				continue
			}
			candidate := time.Date(day.Year(), day.Month(), day.Day(), parsed.Hour(), parsed.Minute(), 0, 0, schedulerLocation())
			if !candidate.After(localNow) {
				continue
			}
			if next.IsZero() || candidate.Before(next) {
				next = candidate
			}
		}
	}
	return next
}

func scheduleSnapshot() map[string]interface{} {
	officialScheduler.mu.Lock()
	defer officialScheduler.mu.Unlock()
	nextRun := nextScheduledRun(officialScheduler.config, time.Now())
	return map[string]interface{}{
		"config": officialScheduler.config, "running": officialScheduler.running,
		"lastRun": formatScheduleTime(officialScheduler.lastRun), "lastSuccess": formatScheduleTime(officialScheduler.lastSuccess),
		"lastError": officialScheduler.lastError, "nextRun": formatScheduleTime(nextRun),
		"tokenConfigured": officialScheduler.tokenConfigured,
	}
}

func formatScheduleTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.In(schedulerLocation()).Format("2006-01-02 15:04:05")
}

func startScheduler() {
	officialScheduler.mu.Lock()
	officialScheduler.config = loadScheduleConfig()
	if data, err := os.ReadFile(schedulerTokenPath()); err == nil {
		officialScheduler.tokenConfigured = strings.TrimSpace(string(data)) != ""
	}
	cfg := officialScheduler.config
	officialScheduler.mu.Unlock()
	fmt.Printf("[schedule] enabled=%v times=%s timezone=%s\n", cfg.Enabled, strings.Join(cfg.Times, ","), schedulerTimezone)
	safeGo("official-scheduler", nil, func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for now := range ticker.C {
			checkScheduledRun(now)
		}
	})
}

func checkScheduledRun(now time.Time) {
	localNow := now.In(schedulerLocation())
	minute := localNow.Format("15:04")
	triggerKey := localNow.Format("2006-01-02 15:04")
	officialScheduler.mu.Lock()
	cfg := officialScheduler.config
	shouldRun := cfg.Enabled && !officialScheduler.running && officialScheduler.lastTriggerKey != triggerKey
	matched := false
	for _, value := range cfg.Times {
		if value == minute {
			matched = true
			break
		}
	}
	if shouldRun && matched {
		officialScheduler.lastTriggerKey = triggerKey
	}
	officialScheduler.mu.Unlock()
	if shouldRun && matched {
		startScheduledRun("定时触发")
	}
}

func startScheduledRun(reason string) error {
	if anyTaskRunning() {
		officialScheduler.mu.Lock()
		officialScheduler.lastError = reason + "失败：已有任务正在运行"
		officialScheduler.mu.Unlock()
		return errors.New("已有任务正在运行，请等待完成后再试")
	}
	officialScheduler.mu.Lock()
	if officialScheduler.running {
		officialScheduler.mu.Unlock()
		return errors.New("定时任务已经在运行")
	}
	cfg := officialScheduler.config
	officialScheduler.running = true
	officialScheduler.lastRun = time.Now()
	officialScheduler.lastError = ""
	officialScheduler.mu.Unlock()
	safeGo("scheduled-official-run", nil, func() {
		err := runScheduledOfficial(cfg)
		officialScheduler.mu.Lock()
		officialScheduler.running = false
		if err != nil {
			officialScheduler.lastError = err.Error()
			fmt.Printf("[schedule] %s失败: %v\n", reason, err)
		} else {
			officialScheduler.lastSuccess = time.Now()
			officialScheduler.lastError = ""
			fmt.Printf("[schedule] %s完成\n", reason)
		}
		officialScheduler.mu.Unlock()
	})
	return nil
}

func runScheduledOfficial(cfg scheduleConfig) error {
	tokenBytes, err := os.ReadFile(schedulerTokenPath())
	if err != nil || strings.TrimSpace(string(tokenBytes)) == "" {
		return errors.New("定时任务未配置 GitHub Token")
	}
	parts := strings.Split(cfg.GitHubRepo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return errors.New("定时任务 GitHub 仓库格式应为 owner/repo")
	}
	outFile := "scheduled-results." + cfg.Format
	cliCfg := &cliConfig{
		enabled: true, configResolved: true, mode: "official", scanMode: cfg.ScanMode,
		ipType: cfg.IPType, threads: cfg.Threads, port: cfg.Port, delay: cfg.Delay,
		dc: cfg.TargetDC, outFile: outFile, speedLimit: cfg.SpeedLimit, speedMin: cfg.SpeedMin,
		speedURL: cfg.SpeedURL, lossMax: cfg.LossMax, officialQualified: true,
		showProgress: false, noColor: true,
		export: cliExportConfig{
			Format: cfg.Format, Fields: "compact", GitHub: true, GitHubSet: true,
			GHRepo: cfg.GitHubRepo, GHBranch: cfg.GitHubBranch, GHPath: cfg.GitHubPath,
			GHMessage: cfg.GitHubMessage, GHToken: strings.TrimSpace(string(tokenBytes)),
		},
	}
	return runOfficialCLI(cliCfg)
}
