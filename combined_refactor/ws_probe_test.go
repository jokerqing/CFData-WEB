package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestNormalizeWSProbeConfig(t *testing.T) {
	cfg, err := normalizeWSProbeConfig(wsProbeConfig{Enabled: true, Host: "example.com", Path: "tunnel"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Path != "/tunnel" || cfg.Attempts != defaultWSProbeAttempts || cfg.TimeoutMS != defaultWSProbeTimeoutMS || cfg.Workers != defaultWSProbeWorkers {
		t.Fatalf("unexpected normalized config: %#v", cfg)
	}
	if _, err := normalizeWSProbeConfig(wsProbeConfig{Enabled: true, Host: "https://example.com", Path: "/"}); err == nil {
		t.Fatal("expected host with scheme to be rejected")
	}
	if _, err := normalizeWSProbeConfig(wsProbeConfig{Enabled: true, Host: "example.com", Path: "http://other.example/path"}); err == nil {
		t.Fatal("expected absolute websocket path to be rejected")
	}
	for _, invalid := range []int{-1, 60001} {
		if _, err := normalizeWSProbeConfig(wsProbeConfig{Enabled: true, Host: "example.com", MaxLatencyMS: invalid}); err == nil {
			t.Fatalf("expected invalid average latency limit %d to be rejected", invalid)
		}
	}
}

func TestWebSocketAverageLatencyIsSeparateFromTimeout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		delays  []time.Duration
		limit   int
		healthy bool
	}{
		{"one slow round but acceptable average", []time.Duration{900 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond}, 800, true},
		{"slow average rejected", []time.Duration{60 * time.Millisecond, 60 * time.Millisecond, 60 * time.Millisecond}, 30, false},
		{"no latency limit preserves legacy behavior", []time.Duration{60 * time.Millisecond, 60 * time.Millisecond, 60 * time.Millisecond}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				index := int(calls.Add(1)) - 1
				time.Sleep(tc.delays[min(index, len(tc.delays)-1)])
				conn, err := upgrader.Upgrade(w, r, nil)
				if err == nil {
					defer conn.Close()
					_, _, _ = conn.ReadMessage()
				}
			}))
			defer server.Close()
			cert := server.Certificate()
			roots := x509.NewCertPool()
			roots.AddCert(cert)
			host, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
			port, _ := strconv.Atoi(portText)
			cfg := wsProbeConfig{Enabled: true, Host: cert.DNSNames[0], Path: "/", Attempts: 3, TimeoutMS: 2000, MaxLatencyMS: tc.limit, Workers: 1}
			result := probeWebSocketEndpointWithTLSConfig(context.Background(), host, port, cfg, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
			if result.Healthy != tc.healthy || result.Successes != 3 || calls.Load() != 3 {
				t.Fatalf("unexpected outcome: %#v calls=%d", result, calls.Load())
			}
		})
	}
}

func TestWebSocketConsecutiveProbeStopsAfterFirstFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.NotFound(w, r) }))
	defer server.Close()
	cert := server.Certificate()
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	host, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	cfg := wsProbeConfig{Enabled: true, Host: cert.DNSNames[0], Path: "/", Attempts: 3, TimeoutMS: 2000, Workers: 1}
	result := probeWebSocketEndpointWithTLSConfig(context.Background(), host, port, cfg, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	if result.Healthy || result.Successes != 0 || calls.Load() != 1 {
		t.Fatalf("must stop after the first failed handshake: %#v calls=%d", result, calls.Load())
	}
}

func TestProbeWebSocketEndpointRealUpgrade(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	var expectedHost string
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != expectedHost || r.URL.Path != "/tunnel" || r.URL.Query().Get("mode") != "probe" {
			http.Error(w, "unexpected target", http.StatusBadRequest)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
	}))
	server.StartTLS()
	defer server.Close()

	cert := server.Certificate()
	expectedHost = "example.com"
	if len(cert.DNSNames) > 0 {
		expectedHost = cert.DNSNames[0]
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	tlsConfig := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}

	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	cfg := wsProbeConfig{Enabled: true, Host: expectedHost, Path: "/tunnel?mode=probe", Attempts: 2, TimeoutMS: 2000, Workers: 1}
	outcome := probeWebSocketEndpointWithTLSConfig(context.Background(), host, port, cfg, tlsConfig)
	if !outcome.Healthy || outcome.Successes != 2 || outcome.Attempts != 2 || outcome.AvgDuration <= 0 || outcome.LastError != "" {
		t.Fatalf("unexpected probe outcome: %#v", outcome)
	}
}

func TestProbeWebSocketEndpointRejectsNonUpgrade(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	cert := server.Certificate()
	hostName := "example.com"
	if len(cert.DNSNames) > 0 {
		hostName = cert.DNSNames[0]
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	host, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	cfg := wsProbeConfig{Enabled: true, Host: hostName, Path: "/missing", Attempts: 1, TimeoutMS: int((2 * time.Second) / time.Millisecond), Workers: 1}
	outcome := probeWebSocketEndpointWithTLSConfig(context.Background(), host, port, cfg, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	if outcome.Healthy || outcome.Successes != 0 || outcome.LastError == "" {
		t.Fatalf("expected failed upgrade, got %#v", outcome)
	}
}
