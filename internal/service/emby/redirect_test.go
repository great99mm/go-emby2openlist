package emby

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
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
		PathMap: []string{fmt.Sprintf("https://strm.example/media => %s/d", server.URL)},
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
		link     string
		resolved bool
		err      error
	}
	results := make(chan result, 2)
	for _, userAgent := range []string{"strmpro-user-a", "strmpro-user-b"} {
		go func() {
			link, resolved, err := resolveStrmLink(
				"https://strm.example/media/%E8%B7%AF%E5%BE%84/video%20name.mkv?sign=expired:0",
				http.Header{"User-Agent": []string{userAgent}},
			)
			results <- result{link: link, resolved: resolved, err: err}
		}()
	}

	links := make(map[string]struct{}, 2)
	for range 2 {
		res := <-results
		if res.err != nil {
			t.Fatalf("resolveStrmLink() error = %v", res.err)
		}
		if !res.resolved {
			t.Fatal("resolveStrmLink() resolved = false, want true")
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

	got, resolved, err := resolveStrmLink("https://old.example/video.mkv", nil)
	if err != nil {
		t.Fatalf("resolveStrmLink() error = %v", err)
	}
	if resolved {
		t.Fatal("resolveStrmLink() resolved = true, want false")
	}
	if want := "https://new.example/video.mkv"; got != want {
		t.Fatalf("resolveStrmLink() = %q, want %q", got, want)
	}
}

func TestResolveStrmLinkWithForeignHost(t *testing.T) {
	var openlistCalls atomic.Int32
	openlistServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openlistCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(openlistServer.Close)

	var foreignCalls atomic.Int32
	foreignServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(foreignServer.Close)
	foreignURL := strings.Replace(foreignServer.URL, "127.0.0.1", "localhost", 1) + "/d/path/video.mkv"

	oldConfig := config.C
	t.Cleanup(func() { config.C = oldConfig })
	config.C = &config.Config{
		Emby: &config.Emby{Strm: &config.Strm{
			StrmPro:                true,
			InternalRedirectEnable: true,
		}},
		Openlist: &config.Openlist{
			Host:  openlistServer.URL,
			Token: "test-token",
		},
	}

	got, resolved, err := resolveStrmLink(foreignURL, nil)
	if err != nil {
		t.Fatalf("resolveStrmLink() error = %v", err)
	}
	if resolved {
		t.Fatal("resolveStrmLink() resolved = true, want false")
	}
	if got != foreignURL {
		t.Fatalf("resolveStrmLink() = %q, want %q", got, foreignURL)
	}
	if openlistCalls.Load() != 0 {
		t.Fatalf("Openlist API calls = %d, want 0", openlistCalls.Load())
	}
	if foreignCalls.Load() != 0 {
		t.Fatalf("foreign host calls = %d, want 0", foreignCalls.Load())
	}
}

func TestResolveStrmLinkWithMatchedOpenlistInstance(t *testing.T) {
	var otherCalls atomic.Int32
	otherServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(otherServer.Close)

	var matchedCalls atomic.Int32
	matchedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		matchedCalls.Add(1)
		if got := r.Header.Get("Authorization"); got != "matched-token" {
			t.Errorf("Authorization = %q, want %q", got, "matched-token")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":200,"message":"success","data":{"raw_url":"https://storage.example/matched.mkv"}}`)
	}))
	t.Cleanup(matchedServer.Close)

	oldConfig := config.C
	t.Cleanup(func() { config.C = oldConfig })
	config.C = &config.Config{
		Emby: &config.Emby{Strm: &config.Strm{
			StrmPro: true,
			StrmProInstances: []config.OpenlistInstance{
				{Host: otherServer.URL, Token: "other-token"},
				{Host: strings.Replace(matchedServer.URL, "127.0.0.1", "localhost", 1), Token: "matched-token"},
			},
		}},
		Openlist: &config.Openlist{
			Host:  otherServer.URL,
			Token: "legacy-token",
		},
	}

	matchedURL := strings.Replace(matchedServer.URL, "127.0.0.1", "localhost", 1) + "/d/path/video.mkv"
	got, resolved, err := resolveStrmLink(matchedURL, nil)
	if err != nil {
		t.Fatalf("resolveStrmLink() error = %v", err)
	}
	if !resolved {
		t.Fatal("resolveStrmLink() resolved = false, want true")
	}
	if got != "https://storage.example/matched.mkv" {
		t.Fatalf("resolveStrmLink() = %q, want %q", got, "https://storage.example/matched.mkv")
	}
	if otherCalls.Load() != 0 || matchedCalls.Load() != 1 {
		t.Fatalf("Openlist API calls = other %d, matched %d; want other 0, matched 1", otherCalls.Load(), matchedCalls.Load())
	}
}

func TestResolveStrmLinkWithInstanceInternalRedirect(t *testing.T) {
	finalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(finalServer.Close)

	redirectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, finalServer.URL+"/video.mkv", http.StatusFound)
	}))
	t.Cleanup(redirectServer.Close)

	openlistServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":200,"message":"success","data":{"raw_url":%q}}`, redirectServer.URL+"/redirect")
	}))
	t.Cleanup(openlistServer.Close)

	enabled := true
	oldConfig := config.C
	t.Cleanup(func() { config.C = oldConfig })
	config.C = &config.Config{
		Emby: &config.Emby{Strm: &config.Strm{
			StrmPro: true,
			StrmProInstances: []config.OpenlistInstance{
				{Host: openlistServer.URL, Token: "test-token", InternalRedirectEnable: &enabled},
			},
		}},
		Openlist: &config.Openlist{},
	}

	got, resolved, err := resolveStrmLink(openlistServer.URL+"/d/path/video.mkv", nil)
	if err != nil {
		t.Fatalf("resolveStrmLink() error = %v", err)
	}
	if !resolved {
		t.Fatal("resolveStrmLink() resolved = false, want true")
	}
	if want := finalServer.URL + "/video.mkv"; got != want {
		t.Fatalf("resolveStrmLink() = %q, want %q", got, want)
	}
}

func TestResolveStrmLinkLoadBalanceAndSpecificRule(t *testing.T) {
	// 使用三个独立本地测试服务分别模拟 fr、de、ch1，并通过不同主机别名区分实例。
	var frCalls, deCalls, ch1Calls atomic.Int32

	frServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		frCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":200,"message":"success","data":{"raw_url":"https://fr-storage.example/mplocal.mkv"}}`)
	}))
	t.Cleanup(frServer.Close)

	deServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":200,"message":"success","data":{"raw_url":"https://de-storage.example/video.mkv"}}`)
	}))
	t.Cleanup(deServer.Close)

	ch1Listener, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Fatalf("listen 127.0.0.2: %v", err)
	}
	ch1Server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ch1Calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":200,"message":"success","data":{"raw_url":"https://ch1-storage.example/video.mkv"}}`)
	}))
	ch1Server.Listener = ch1Listener
	ch1Server.Start()
	t.Cleanup(ch1Server.Close)

	// MatchOpenlistInstance 按主机名匹配，使用 localhost / 127.0.0.1 / 127.0.0.2 区分三个实例。
	frHost := strings.Replace(frServer.URL, "127.0.0.1", "localhost", 1)
	deHost := deServer.URL
	ch1Host := ch1Server.URL

	strmConfig := &config.Strm{
		PathMap: []string{
			fmt.Sprintf("http://nl.example/d/mplocal/ => %s/d/mplocal/", frHost),
			fmt.Sprintf("http://nl.example/ => %s/ | %s/", deHost, ch1Host),
		},
		StrmPro: true,
		StrmProInstances: []config.OpenlistInstance{
			{Host: frHost, Token: "shared-token"},
			{Host: deHost, Token: "shared-token"},
			{Host: ch1Host, Token: "shared-token"},
		},
	}
	if err := strmConfig.Init(); err != nil {
		t.Fatalf("init strm config: %v", err)
	}

	oldConfig := config.C
	t.Cleanup(func() { config.C = oldConfig })
	config.C = &config.Config{
		Emby:     &config.Emby{Strm: strmConfig},
		Openlist: &config.Openlist{},
	}

	// 验证 mplocal 始终只命中 fr 实例。
	for range 10 {
		got, resolved, err := resolveStrmLink("http://nl.example/d/mplocal/test.mkv", nil)
		if err != nil {
			t.Fatalf("resolveStrmLink(mplocal) error = %v", err)
		}
		if !resolved || got != "https://fr-storage.example/mplocal.mkv" {
			t.Fatalf("resolveStrmLink(mplocal) = (%q, %v), want fr link", got, resolved)
		}
	}
	if frCalls.Load() != 10 || deCalls.Load() != 0 || ch1Calls.Load() != 0 {
		t.Fatalf("mplocal calls = fr:%d de:%d ch1:%d, want fr:10 de:0 ch1:0", frCalls.Load(), deCalls.Load(), ch1Calls.Load())
	}

	// 验证其余路径随机均衡打到 de 与 ch1，不打到 fr。
	const generalRuns = 60
	for range generalRuns {
		got, resolved, err := resolveStrmLink("http://nl.example/d/cloudreve-upload/test.mp4", nil)
		if err != nil {
			t.Fatalf("resolveStrmLink(general) error = %v", err)
		}
		if !resolved || (got != "https://de-storage.example/video.mkv" && got != "https://ch1-storage.example/video.mkv") {
			t.Fatalf("resolveStrmLink(general) = (%q, %v), want de or ch1 link", got, resolved)
		}
	}
	if frCalls.Load() != 10 {
		t.Fatalf("general path unexpectedly hit fr: frCalls=%d, want 10", frCalls.Load())
	}
	if deCalls.Load() == 0 || ch1Calls.Load() == 0 || deCalls.Load()+ch1Calls.Load() != generalRuns {
		t.Fatalf("load balance calls = de:%d ch1:%d, want both > 0 and sum=%d", deCalls.Load(), ch1Calls.Load(), generalRuns)
	}
}

func TestResolveStrmLinkLoadBalanceFailover(t *testing.T) {
	var brokenCalls, healthyCalls atomic.Int32

	brokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		brokenCalls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(brokenServer.Close)

	healthyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		healthyCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":200,"message":"success","data":{"raw_url":"https://healthy-storage.example/video.mkv"}}`)
	}))
	t.Cleanup(healthyServer.Close)

	brokenHost := brokenServer.URL
	healthyHost := strings.Replace(healthyServer.URL, "127.0.0.1", "localhost", 1)

	strmConfig := &config.Strm{
		PathMap: []string{
			fmt.Sprintf("http://nl.example/ => %s/ | %s/", brokenHost, healthyHost),
		},
		StrmPro: true,
		StrmProInstances: []config.OpenlistInstance{
			{Host: brokenHost, Token: "shared-token"},
			{Host: healthyHost, Token: "shared-token"},
		},
	}
	if err := strmConfig.Init(); err != nil {
		t.Fatalf("init strm config: %v", err)
	}

	oldConfig := config.C
	t.Cleanup(func() { config.C = oldConfig })
	config.C = &config.Config{
		Emby:     &config.Emby{Strm: strmConfig},
		Openlist: &config.Openlist{},
	}

	// 多次请求覆盖两种随机顺序，无论是否先命中故障节点都应自动回退成功。
	const runs = 30
	for range runs {
		got, resolved, err := resolveStrmLink("http://nl.example/d/cloudreve-upload/test.mp4", nil)
		if err != nil {
			t.Fatalf("resolveStrmLink() error = %v, want failover success", err)
		}
		if !resolved || got != "https://healthy-storage.example/video.mkv" {
			t.Fatalf("resolveStrmLink() = (%q, %v), want healthy link", got, resolved)
		}
	}
	if healthyCalls.Load() != runs {
		t.Fatalf("healthyCalls = %d, want %d", healthyCalls.Load(), runs)
	}
	if brokenCalls.Load() == 0 {
		t.Fatalf("brokenCalls = 0, expected random selection to exercise failover path")
	}
}

func TestResolveStrmLinkLoadBalanceAllFail(t *testing.T) {
	var firstCalls, secondCalls atomic.Int32

	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(firstServer.Close)

	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(secondServer.Close)

	firstHost := firstServer.URL
	secondHost := strings.Replace(secondServer.URL, "127.0.0.1", "localhost", 1)

	strmConfig := &config.Strm{
		PathMap: []string{
			fmt.Sprintf("http://nl.example/ => %s/ | %s/", firstHost, secondHost),
		},
		StrmPro: true,
		StrmProInstances: []config.OpenlistInstance{
			{Host: firstHost, Token: "shared-token"},
			{Host: secondHost, Token: "shared-token"},
		},
	}
	if err := strmConfig.Init(); err != nil {
		t.Fatalf("init strm config: %v", err)
	}

	oldConfig := config.C
	t.Cleanup(func() { config.C = oldConfig })
	config.C = &config.Config{
		Emby:     &config.Emby{Strm: strmConfig},
		Openlist: &config.Openlist{},
	}

	_, resolved, err := resolveStrmLink("http://nl.example/d/cloudreve-upload/test.mp4", nil)
	if err == nil {
		t.Fatal("resolveStrmLink() error = nil, want error when all candidates fail")
	}
	if resolved {
		t.Fatal("resolveStrmLink() resolved = true, want false")
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 1 {
		t.Fatalf("calls = first:%d second:%d, want both 1", firstCalls.Load(), secondCalls.Load())
	}
}

func TestResolveStrmLinkPriorityDistributionAndZeroDisable(t *testing.T) {
	var deCalls, ch1Calls atomic.Int32

	deServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":200,"message":"success","data":{"raw_url":"https://de-storage.example/video.mkv"}}`)
	}))
	t.Cleanup(deServer.Close)

	ch1Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ch1Calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":200,"message":"success","data":{"raw_url":"https://ch1-storage.example/video.mkv"}}`)
	}))
	t.Cleanup(ch1Server.Close)

	deHost := deServer.URL
	ch1Host := strings.Replace(ch1Server.URL, "127.0.0.1", "localhost", 1)

	dePriority := 70
	ch1Priority := 30
	strmConfig := &config.Strm{
		PathMap: []string{
			fmt.Sprintf("http://nl.example/ => %s/ | %s/", deHost, ch1Host),
		},
		StrmPro: true,
		StrmProInstances: []config.OpenlistInstance{
			{Host: deHost, Token: "shared-token", Priority: &dePriority},
			{Host: ch1Host, Token: "shared-token", Priority: &ch1Priority},
		},
	}
	if err := strmConfig.Init(); err != nil {
		t.Fatalf("init strm config: %v", err)
	}

	oldConfig := config.C
	t.Cleanup(func() { config.C = oldConfig })
	config.C = &config.Config{
		Emby:     &config.Emby{Strm: strmConfig},
		Openlist: &config.Openlist{},
	}

	const runs = 200
	for range runs {
		got, resolved, err := resolveStrmLink("http://nl.example/d/cloudreve-upload/test.mp4", nil)
		if err != nil {
			t.Fatalf("resolveStrmLink() error = %v", err)
		}
		if !resolved || (got != "https://de-storage.example/video.mkv" && got != "https://ch1-storage.example/video.mkv") {
			t.Fatalf("resolveStrmLink() = (%q, %v), want de or ch1 link", got, resolved)
		}
	}
	if deCalls.Load() != 140 || ch1Calls.Load() != 60 {
		t.Fatalf("weighted SWRR calls = de:%d ch1:%d out of %d, want 140/60", deCalls.Load(), ch1Calls.Load(), runs)
	}

	// 将 ch1 的 priority 置 0 后，请求应全部走 de，不再打到 ch1。
	deCalls.Store(0)
	ch1Calls.Store(0)
	zero := 0
	strmConfig.StrmProInstances[1].Priority = &zero
	for range 20 {
		got, resolved, err := resolveStrmLink("http://nl.example/d/cloudreve-upload/test.mp4", nil)
		if err != nil {
			t.Fatalf("resolveStrmLink(priority=0) error = %v", err)
		}
		if !resolved || got != "https://de-storage.example/video.mkv" {
			t.Fatalf("resolveStrmLink(priority=0) = (%q, %v), want de link", got, resolved)
		}
	}
	if deCalls.Load() != 20 || ch1Calls.Load() != 0 {
		t.Fatalf("disabled ch1 calls = de:%d ch1:%d, want de:20 ch1:0", deCalls.Load(), ch1Calls.Load())
	}
}

func TestResolveStrmLinkStickyAndFailoverUpdate(t *testing.T) {
	var deCalls, ch1Calls atomic.Int32
	var deDown atomic.Bool

	deServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deCalls.Add(1)
		if deDown.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":200,"message":"success","data":{"raw_url":"https://de-storage.example/video.mkv"}}`)
	}))
	t.Cleanup(deServer.Close)

	ch1Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ch1Calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":200,"message":"success","data":{"raw_url":"https://ch1-storage.example/video.mkv"}}`)
	}))
	t.Cleanup(ch1Server.Close)

	deHost := deServer.URL
	ch1Host := strings.Replace(ch1Server.URL, "127.0.0.1", "localhost", 1)
	dePriority := 70
	ch1Priority := 30

	strmConfig := &config.Strm{
		PathMap: []string{
			fmt.Sprintf("http://nl.example/ => %s/ | %s/", deHost, ch1Host),
		},
		StrmPro: true,
		StrmProInstances: []config.OpenlistInstance{
			{Host: deHost, Token: "shared-token", Priority: &dePriority},
			{Host: ch1Host, Token: "shared-token", Priority: &ch1Priority},
		},
		StickyTTL:   "30m",
		StickyScope: config.StickyScopeUserItem,
	}
	if err := strmConfig.Init(); err != nil {
		t.Fatalf("init strm config: %v", err)
	}

	oldConfig := config.C
	t.Cleanup(func() { config.C = oldConfig })
	config.C = &config.Config{
		Emby:     &config.Emby{Strm: strmConfig},
		Openlist: &config.Openlist{},
	}

	// 用户 A 首次播放命中 de，随后连续拖动进度条 5 次均应固定走 de。
	userAKey := "token-user-a|item-926878:ms-1"
	for range 6 {
		got, resolved, err := resolveStrmLinkWithSticky("http://nl.example/d/cloudreve-upload/test.mp4", nil, userAKey)
		if err != nil || !resolved || got != "https://de-storage.example/video.mkv" {
			t.Fatalf("user A got (%q, %v, %v), want de", got, resolved, err)
		}
	}
	if deCalls.Load() != 6 || ch1Calls.Load() != 0 {
		t.Fatalf("sticky calls = de:%d ch1:%d, want de:6 ch1:0", deCalls.Load(), ch1Calls.Load())
	}

	// 模拟 de 突然故障：用户 A 再次拖动进度条应自动回退到 ch1 并更新粘性记录。
	deDown.Store(true)
	got, resolved, err := resolveStrmLinkWithSticky("http://nl.example/d/cloudreve-upload/test.mp4", nil, userAKey)
	if err != nil || !resolved || got != "https://ch1-storage.example/video.mkv" {
		t.Fatalf("failover got (%q, %v, %v), want ch1", got, resolved, err)
	}
	if deCalls.Load() != 7 || ch1Calls.Load() != 1 {
		t.Fatalf("after failover calls = de:%d ch1:%d, want de:7 ch1:1", deCalls.Load(), ch1Calls.Load())
	}

	// 后续拖动进度条应直接命中已更新的粘性节点 ch1，不再重复打到故障的 de。
	for range 3 {
		got, resolved, err = resolveStrmLinkWithSticky("http://nl.example/d/cloudreve-upload/test.mp4", nil, userAKey)
		if err != nil || !resolved || got != "https://ch1-storage.example/video.mkv" {
			t.Fatalf("post-failover sticky got (%q, %v, %v), want ch1", got, resolved, err)
		}
	}
	if deCalls.Load() != 7 || ch1Calls.Load() != 4 {
		t.Fatalf("post-failover calls = de:%d ch1:%d, want de:7 ch1:4", deCalls.Load(), ch1Calls.Load())
	}
}

func TestBuildStrmStickyKey(t *testing.T) {
	oldConfig := config.C
	t.Cleanup(func() { config.C = oldConfig })

	item := ItemInfo{
		Id:     "926878",
		ApiKey: "user-token-1",
		MsInfo: MsInfo{OriginId: "mediasource_1"},
	}

	config.C = &config.Config{Emby: &config.Emby{Strm: &config.Strm{StickyScope: config.StickyScopeUserItem}}}
	if got := buildStrmStickyKey(nil, item); got != "user-token-1|926878:mediasource_1" {
		t.Fatalf("user-item key = %q, want %q", got, "user-token-1|926878:mediasource_1")
	}

	config.C = &config.Config{Emby: &config.Emby{Strm: &config.Strm{StickyScope: config.StickyScopeUser}}}
	if got := buildStrmStickyKey(nil, item); got != "user-token-1" {
		t.Fatalf("user key = %q, want %q", got, "user-token-1")
	}
}
