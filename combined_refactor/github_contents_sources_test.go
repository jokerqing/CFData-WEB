package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type sourceURLTransport func(*http.Request) (*http.Response, error)

func (f sourceURLTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGitHubContentsSourceReturnsRawIPList(t *testing.T) {
	old := upstreamHTTPClient
	t.Cleanup(func() { upstreamHTTPClient = old })
	for _, tc := range []struct {
		url string
		raw bool
	}{
		{"https://api.github.com/repos/ymyuuu/IPDB/contents/BestCF/bestcfv4.txt?ref=main", true},
		{"https://api.github.com/repos/love-ztm/cfip/contents/ubest_ips.txt?ref=main", true},
		{"https://api.github.com/repos/owner/repo/releases/latest", false},
		{"https://api.github.com/repos/owner/repo/contents", false},
		{"https://api.github.com.example/repos/owner/repo/contents/ip.txt", false},
		{"https://bestcf.pages.dev/xinyitang3/ipv4.txt", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			const list = "104.16.1.1:443\n104.16.1.2:443\n"
			upstreamHTTPClient = &http.Client{Transport: sourceURLTransport(func(r *http.Request) (*http.Response, error) {
				if got := r.Header.Get("Accept"); (got == "application/vnd.github.raw+json") != tc.raw {
					t.Fatalf("unexpected raw content negotiation: %q", got)
				}
				if tc.raw && !strings.HasPrefix(r.Header.Get("User-Agent"), "CFData-WEB/") {
					t.Fatal("GitHub source request has no application user agent")
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(list)), Header: make(http.Header)}, nil
			})}
			got, err := getURLContentWithContext(context.Background(), tc.url)
			if err != nil || got != list {
				t.Fatalf("IP list was not preserved: %q, %v", got, err)
			}
		})
	}
}
