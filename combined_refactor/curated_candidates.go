package main

import (
	"net/http"
	"os"
	"path/filepath"
)

// Serve only the operator-managed candidate file, never arbitrary runtime files.
func handleCuratedCandidates(w http.ResponseWriter, r *http.Request) {
	serveCuratedCandidates(w, r, filepath.Join(proxySpeedDir(), "cfdata-curated-candidates.txt"))
}

func serveCuratedCandidates(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "candidate list unavailable", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "candidate list unavailable", http.StatusInternalServerError)
		return
	}
	if info.Size() > 64*1024 {
		http.Error(w, "candidate list too large", http.StatusRequestEntityTooLarge)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "curated-candidates.txt", info.ModTime(), file)
}
