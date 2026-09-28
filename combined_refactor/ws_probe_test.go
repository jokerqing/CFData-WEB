package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
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
