package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
)

func parseNetworkSourceURLs(raw string) ([]string, error) {
	seen := map[string]bool{}
	result := make([]string, 0)
	for lineNumber, rawLine := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		value := strings.TrimSpace(rawLine)
		if value == "" || strings.HasPrefix(value, "#") {
			continue
		}
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, fmt.Errorf("第 %d 行不是有效的 http/https 地址", lineNumber+1)
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("请至少填写一个有效的网络 URL；空行和以 # 开头的行会被忽略")
	}
	return result, nil
}

func fetchNetworkSourceURLs(ctx context.Context, raw string) (string, string, error) {
	urls, err := parseNetworkSourceURLs(raw)
	if err != nil {
		return "", "", err
	}
	type fetchResult struct {
		content string
		err     error
	}
	results := make([]fetchResult, len(urls))
	workerLimit := min(len(urls), 8)
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < workerLimit; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				results[idx].content, results[idx].err = getURLContentWithContext(ctx, urls[idx])
			}
		}()
	}
	for idx := range urls {
		select {
		case jobs <- idx:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return "", "", ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()

	contents := make([]string, 0, len(results))
	for idx, result := range results {
		if result.err != nil {
			return "", "", fmt.Errorf("获取第 %d 个网络 URL 失败（%s）: %w", idx+1, urls[idx], result.err)
		}
		contents = append(contents, strings.TrimSpace(result.content))
	}
	return strings.Join(contents, "\n"), strings.Join(urls, ", "), nil
}
