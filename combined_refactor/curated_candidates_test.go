package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCuratedCandidateFileIsServedWithoutCaching(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidates.txt")
	first := "8.8.4.1:443\n8.8.4.2:8443\n"
	if err := os.WriteFile(path, []byte(first), 0600); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		w := httptest.NewRecorder()
		serveCuratedCandidates(w, httptest.NewRequest(method, "/curated-candidates.txt", nil), path)
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unexpected response: %d %v", w.Code, w.Header())
		}
		if method == http.MethodGet && w.Body.String() != first {
			t.Fatalf("wrong pool: %q", w.Body.String())
		}
		if method == http.MethodHead && w.Body.Len() != 0 {
			t.Fatal("HEAD returned a body")
		}
	}
	updated := "8.8.4.3:443\n"
	if err := os.WriteFile(path, []byte(updated), 0600); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	serveCuratedCandidates(w, httptest.NewRequest(http.MethodGet, "/curated-candidates.txt", nil), path)
	if w.Body.String() != updated {
		t.Fatal("candidate update was not reflected")
	}
}

func TestCuratedCandidateEndpointRejectsMissingFilesAndWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.txt")
	for _, tc := range []struct {
		method string
		status int
	}{{http.MethodGet, 404}, {http.MethodPost, 405}, {http.MethodPut, 405}} {
		w := httptest.NewRecorder()
		serveCuratedCandidates(w, httptest.NewRequest(tc.method, "/curated-candidates.txt", nil), path)
		if w.Code != tc.status {
			t.Fatalf("%s: got %d want %d", tc.method, w.Code, tc.status)
		}
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 64*1024+1)), 0600); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	serveCuratedCandidates(w, httptest.NewRequest(http.MethodGet, "/curated-candidates.txt", nil), path)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized pool: %d", w.Code)
	}
}
