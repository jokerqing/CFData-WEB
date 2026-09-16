package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const githubTokenStoreVersion = 1

type githubTokenStoreFile struct {
	Version int               `json:"version"`
	Tokens  map[string]string `json:"tokens"`
}

var githubTokenStoreMu sync.Mutex

func githubTokenStorePath() string {
	return filepath.Join(schedulerBaseDir(), "cfdata-github-tokens.json")
}

func normalizeGitHubRepoKey(owner, repo string) (string, error) {
	owner = strings.Trim(strings.TrimSpace(owner), "/")
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	if owner == "" || repo == "" || strings.Contains(owner, "/") || strings.Contains(repo, "/") {
		return "", errors.New("GitHub 仓库格式应为 owner/repo")
	}
	return strings.ToLower(owner + "/" + repo), nil
}

func splitGitHubRepo(fullRepo string) (string, string, error) {
	parts := strings.Split(strings.Trim(strings.TrimSpace(fullRepo), "/"), "/")
	if len(parts) != 2 {
		return "", "", errors.New("GitHub 仓库格式应为 owner/repo")
	}
	if _, err := normalizeGitHubRepoKey(parts[0], parts[1]); err != nil {
		return "", "", err
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

func loadGitHubTokenStoreAt(path string) (githubTokenStoreFile, error) {
	store := githubTokenStoreFile{Version: githubTokenStoreVersion, Tokens: map[string]string{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return store, err
	}
	if err := json.Unmarshal(data, &store); err != nil {
		return store, fmt.Errorf("解析 GitHub Token 文件失败: %w", err)
	}
	if store.Tokens == nil {
		store.Tokens = map[string]string{}
	}
	store.Version = githubTokenStoreVersion
	return store, nil
}

func writeGitHubTokenStoreAt(path string, store githubTokenStoreFile) error {
	store.Version = githubTokenStoreVersion
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".cfdata-github-tokens-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		// Windows cannot replace an existing file with Rename. The production
		// container uses Linux, where the first rename is atomic.
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return err
		}
		if retryErr := os.Rename(tmpPath, path); retryErr != nil {
			return retryErr
		}
	}
	return os.Chmod(path, 0o600)
}

func saveGitHubToken(owner, repo, token string) error {
	key, err := normalizeGitHubRepoKey(owner, repo)
	if err != nil {
		return err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("GitHub Token 不能为空")
	}
	githubTokenStoreMu.Lock()
	defer githubTokenStoreMu.Unlock()
	store, err := loadGitHubTokenStoreAt(githubTokenStorePath())
	if err != nil {
		return err
	}
	store.Tokens[key] = token
	return writeGitHubTokenStoreAt(githubTokenStorePath(), store)
}

func loadGitHubToken(owner, repo string) (string, error) {
	key, err := normalizeGitHubRepoKey(owner, repo)
	if err != nil {
		return "", err
	}
	githubTokenStoreMu.Lock()
	defer githubTokenStoreMu.Unlock()
	store, err := loadGitHubTokenStoreAt(githubTokenStorePath())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(store.Tokens[key]), nil
}

func loadGitHubTokenForFullRepo(fullRepo string) (string, error) {
	owner, repo, err := splitGitHubRepo(fullRepo)
	if err != nil {
		return "", err
	}
	return loadGitHubToken(owner, repo)
}

func prepareGitHubUpload(params *githubUploadRequest) error {
	params.Owner = strings.TrimSpace(params.Owner)
	params.Repo = strings.TrimSpace(params.Repo)
	if _, err := normalizeGitHubRepoKey(params.Owner, params.Repo); err != nil {
		return err
	}
	params.Token = strings.TrimSpace(params.Token)
	if params.Token != "" && params.SaveToken {
		if err := saveGitHubToken(params.Owner, params.Repo, params.Token); err != nil {
			return fmt.Errorf("保存 GitHub Token 失败: %w", err)
		}
	}
	if params.Token == "" {
		token, err := loadGitHubToken(params.Owner, params.Repo)
		if err != nil {
			return fmt.Errorf("读取 GitHub Token 失败: %w", err)
		}
		params.Token = token
	}
	if params.Token == "" {
		return errors.New("该仓库尚未在服务器保存 GitHub Token，请输入一次 Token 并勾选永久保存")
	}
	return nil
}
