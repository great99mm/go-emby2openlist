package emby

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/AmbitiousJun/go-emby2openlist/v2/internal/config"
)

func TestOpenlistPathFromStrmURL(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		want    string
		wantErr bool
	}{
		{
			name:   "encoded openlist url",
			rawURL: "https://opl.ftp2.eu.org/d/%E8%B0%B7%E6%AD%8C/%E5%8A%A8%E7%94%BB%E7%94%B5%E5%BD%B1/%E5%8F%98%E5%BD%A2%E9%87%91%E5%88%9A%EF%BC%9A%E8%B5%B7%E6%BA%90-2024-%5Btmdb=698687%5D/%E5%8F%98%E5%BD%A2%E9%87%91%E5%88%9A%EF%BC%9A%E8%B5%B7%E6%BA%90.2024.2160p.BluRay.DV.HDR.REMUX.TrueHD.7.1.mkv",
			want:   "/谷歌/动画电影/变形金刚：起源-2024-[tmdb=698687]/变形金刚：起源.2024.2160p.BluRay.DV.HDR.REMUX.TrueHD.7.1.mkv",
		},
		{
			name:   "base path and expired sign",
			rawURL: "https://openlist.example/base/d/%E8%B7%AF%E5%BE%84/video%20name.mkv?sign=expired:0",
			want:   "/路径/video name.mkv",
		},
		{
			name:    "not an openlist download url",
			rawURL:  "https://storage.example/video.mkv",
			wantErr: true,
		},
		{
			name:    "missing resource path",
			rawURL:  "https://openlist.example/d/",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := openlistPathFromStrmURL(tt.rawURL)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("openlistPathFromStrmURL() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("openlistPathFromStrmURL() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("openlistPathFromStrmURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveStrmLinkWithStrmPro(t *testing.T) {
	type observedRequest struct {
		method        string
		uri           string
		authorization string
		userAgent     string
		openlistPath  string
		decodeErr     error
	}
	observed := make(chan observedRequest, 2)
	var apiCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := apiCalls.Add(1)
		var body struct {
			Path string `json:"path"`
		}
		decodeErr := json.NewDecoder(r.Body).Decode(&body)
		observed <- observedRequest{
			method:        r.Method,
			uri:           r.URL.Path,
			authorization: r.Header.Get("Authorization"),
			userAgent:     r.Header.Get("User-Agent"),
			openlistPath:  body.Path,
			decodeErr:     decodeErr,
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":200,"message":"success","data":{"raw_url":"https://storage.example/video.mkv?request=%d"}}`, call)
	}))
	t.Cleanup(server.Close)

	strmConfig := &config.Strm{
		PathMap: []string{"https://strm.example/media => https://openlist.example/d"},
		StrmPro: true,
	}
	if err := strmConfig.Init(); err != nil {
		t.Fatalf("init strm config: %v", err)
	}

	oldConfig := config.C
	t.Cleanup(func() { config.C = oldConfig })
	config.C = &config.Config{
		Emby: &config.Emby{Strm: strmConfig},
		Openlist: &config.Openlist{
			Host:  server.URL,
			Token: "test-token",
		},
	}

	type result struct {
		link string
		err  error
	}
	results := make(chan result, 2)
	for _, userAgent := range []string{"strmpro-user-a", "strmpro-user-b"} {
		go func() {
			link, err := resolveStrmLink(
				"https://strm.example/media/%E8%B7%AF%E5%BE%84/video%20name.mkv?sign=expired:0",
				http.Header{"User-Agent": []string{userAgent}},
			)
			results <- result{link: link, err: err}
		}()
	}

	links := make(map[string]struct{}, 2)
	for range 2 {
		res := <-results
		if res.err != nil {
			t.Fatalf("resolveStrmLink() error = %v", res.err)
		}
		links[res.link] = struct{}{}
	}
	if len(links) != 2 || apiCalls.Load() != 2 {
		t.Fatalf("concurrent requests returned %d links from %d Openlist calls, want 2 independent links from 2 calls", len(links), apiCalls.Load())
	}

	userAgents := make(map[string]struct{}, 2)
	for range 2 {
		req := <-observed
		if req.decodeErr != nil {
			t.Fatalf("decode Openlist request: %v", req.decodeErr)
		}
		if req.method != http.MethodPost || req.uri != "/api/fs/get" {
			t.Fatalf("Openlist request = %s %s, want POST /api/fs/get", req.method, req.uri)
		}
		if req.authorization != "test-token" {
			t.Fatalf("Authorization = %q, want %q", req.authorization, "test-token")
		}
		if req.openlistPath != "/路径/video name.mkv" {
			t.Fatalf("Openlist path = %q, want %q", req.openlistPath, "/路径/video name.mkv")
		}
		userAgents[req.userAgent] = struct{}{}
	}
	if len(userAgents) != 2 {
		t.Fatalf("Openlist received %d distinct user agents, want 2", len(userAgents))
	}
}

func TestResolveStrmLinkWithoutStrmPro(t *testing.T) {
	strmConfig := &config.Strm{
		PathMap: []string{"https://old.example => https://new.example"},
	}
	if err := strmConfig.Init(); err != nil {
		t.Fatalf("init strm config: %v", err)
	}

	oldConfig := config.C
	t.Cleanup(func() { config.C = oldConfig })
	config.C = &config.Config{Emby: &config.Emby{Strm: strmConfig}}

	got, err := resolveStrmLink("https://old.example/video.mkv", nil)
	if err != nil {
		t.Fatalf("resolveStrmLink() error = %v", err)
	}
	if want := "https://new.example/video.mkv"; got != want {
		t.Fatalf("resolveStrmLink() = %q, want %q", got, want)
	}
}
