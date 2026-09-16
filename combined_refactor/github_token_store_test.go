package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGitHubTokenStoreRoundTripAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfdata-github-tokens.json")
	store := githubTokenStoreFile{
		Tokens: map[string]string{"jokerqing/cfdata-web": "secret-token"},
	}
	if err := writeGitHubTokenStoreAt(path, store); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadGitHubTokenStoreAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != githubTokenStoreVersion || loaded.Tokens["jokerqing/cfdata-web"] != "secret-token" {
		t.Fatalf("unexpected token store: %#v", loaded)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("token store permissions = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestNormalizeGitHubRepoKey(t *testing.T) {
	key, err := normalizeGitHubRepoKey(" JokerQing ", " CFData-WEB ")
	if err != nil {
		t.Fatal(err)
	}
	if key != "jokerqing/cfdata-web" {
		t.Fatalf("unexpected repository key: %q", key)
	}
	if _, err := normalizeGitHubRepoKey("bad/owner", "repo"); err == nil {
		t.Fatal("expected invalid owner to fail")
	}
}

func TestIndexUsesServerSideGitHubTokenStorage(t *testing.T) {
	data, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{
		"永久保存 token 到 Docker 持久化目录",
		"saveToken: account.saveToken !== false",
		"payload.saveToken = rememberToken",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("index.html missing %q", expected)
		}
	}
	if strings.Contains(text, "无法安全保存 token") {
		t.Fatal("GitHub token flow must not depend on browser WebCrypto")
	}
}
