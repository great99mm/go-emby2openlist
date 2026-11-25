package config

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestStrmInit(t *testing.T) {
	tests := []struct {
		name        string
		pathMap     []string
		stickyTTL   string
		stickyScope StickyScope
		wantRules   []strmPathRule
		wantTTL     time.Duration
		wantScope   StickyScope
		wantErr     bool
	}{
		{
			name: "单目标与多目标规则混合及默认粘性配置",
			pathMap: []string{
				"//nl.ftp2.eu.org/d/mplocal/ => //fr.ftp2.eu.org/d/mplocal/",
				"//nl.ftp2.eu.org/ => //de.ftp2.eu.org/ | //ch1.ftp2.eu.org/",
			},
			wantRules: []strmPathRule{
				{
					from:    "//nl.ftp2.eu.org/d/mplocal/",
					targets: []string{"//fr.ftp2.eu.org/d/mplocal/"},
				},
				{
					from:    "//nl.ftp2.eu.org/",
					targets: []string{"//de.ftp2.eu.org/", "//ch1.ftp2.eu.org/"},
				},
			},
			wantTTL:   defaultStrmStickyTTL,
			wantScope: StickyScopeUserItem,
		},
		{
			name:        "自定义粘性时长与纯用户维度",
			pathMap:     []string{"//nl.ftp2.eu.org/ => //de.ftp2.eu.org/ | //ch1.ftp2.eu.org/"},
			stickyTTL:   "1d",
			stickyScope: StickyScopeUser,
			wantRules: []strmPathRule{
				{
					from:    "//nl.ftp2.eu.org/",
					targets: []string{"//de.ftp2.eu.org/", "//ch1.ftp2.eu.org/"},
				},
			},
			wantTTL:   24 * time.Hour,
			wantScope: StickyScopeUser,
		},
		{
			name:      "关闭会话粘性",
			pathMap:   []string{"//nl.ftp2.eu.org/ => //de.ftp2.eu.org/ | //ch1.ftp2.eu.org/"},
			stickyTTL: "0",
			wantRules: []strmPathRule{
				{
					from:    "//nl.ftp2.eu.org/",
					targets: []string{"//de.ftp2.eu.org/", "//ch1.ftp2.eu.org/"},
				},
			},
			wantTTL:   0,
			wantScope: StickyScopeUserItem,
		},
		{
			name: "重复源片段自动合并并保持首次位置",
			pathMap: []string{
				"//nl.ftp2.eu.org/ => //de.ftp2.eu.org/",
				"//other.example/ => //target.example/",
				"//nl.ftp2.eu.org/ => //ch1.ftp2.eu.org/",
			},
			wantRules: []strmPathRule{
				{
					from:    "//nl.ftp2.eu.org/",
					targets: []string{"//de.ftp2.eu.org/", "//ch1.ftp2.eu.org/"},
				},
				{
					from:    "//other.example/",
					targets: []string{"//target.example/"},
				},
			},
			wantTTL:   defaultStrmStickyTTL,
			wantScope: StickyScopeUserItem,
		},
		{
			name:    "单目标允许空字符串以支持移除前缀",
			pathMap: []string{"/legacy-prefix => "},
			wantRules: []strmPathRule{
				{
					from:    "/legacy-prefix",
					targets: []string{""},
				},
			},
			wantTTL:   defaultStrmStickyTTL,
			wantScope: StickyScopeUserItem,
		},
		{
			name:    "缺少分隔符报错",
			pathMap: []string{"//nl.ftp2.eu.org/"},
			wantErr: true,
		},
		{
			name:    "源片段为空报错",
			pathMap: []string{"  => //de.ftp2.eu.org/"},
			wantErr: true,
		},
		{
			name:    "多目标包含空项报错",
			pathMap: []string{"//nl.ftp2.eu.org/ => //de.ftp2.eu.org/ | "},
			wantErr: true,
		},
		{
			name:      "无效粘性时长报错",
			stickyTTL: "-5m",
			wantErr:   true,
		},
		{
			name:        "无效粘性维度报错",
			stickyScope: "invalid",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Strm{
				PathMap:     tt.pathMap,
				StickyTTL:   tt.stickyTTL,
				StickyScope: tt.stickyScope,
			}
			err := s.Init()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Init() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Init() unexpected error = %v", err)
			}
			if s.stickyTTL != tt.wantTTL {
				t.Fatalf("stickyTTL = %v, want %v", s.stickyTTL, tt.wantTTL)
			}
			if s.StickyScope != tt.wantScope {
				t.Fatalf("StickyScope = %q, want %q", s.StickyScope, tt.wantScope)
			}
			if len(s.pathMap) != len(tt.wantRules) {
				t.Fatalf("Init() rules count = %d, want %d", len(s.pathMap), len(tt.wantRules))
			}
			for i, want := range tt.wantRules {
				got := s.pathMap[i]
				if got.from != want.from || !slices.Equal(got.targets, want.targets) {
					t.Fatalf("rule[%d] = %+v, want %+v", i, got, want)
				}
			}
		})
	}
}

func TestStrmMapPathPriorityAndLoadBalance(t *testing.T) {
	s := &Strm{
		PathMap: []string{
			"//nl.ftp2.eu.org/d/mplocal/ => //fr.ftp2.eu.org/d/mplocal/",
			"//nl.ftp2.eu.org/ => //de.ftp2.eu.org/ | //ch1.ftp2.eu.org/",
		},
	}
	if err := s.Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// mplocal 路径必须固定路由到 fr，且仅返回单个候选。
	mplocalURL := "https://nl.ftp2.eu.org/d/mplocal/movie/demo.mkv"
	wantMplocal := "https://fr.ftp2.eu.org/d/mplocal/movie/demo.mkv"
	for range 20 {
		candidates := s.MapPathCandidates(mplocalURL)
		if !slices.Equal(candidates, []string{wantMplocal}) {
			t.Fatalf("MapPathCandidates(%q) = %v, want [%q]", mplocalURL, candidates, wantMplocal)
		}
	}

	// 默认等权下平滑加权轮询应严格 1:1 交替分布，且每次均包含另一节点作为回退。
	generalURL := "https://nl.ftp2.eu.org/d/cloudreve-upload/movie/demo.mp4"
	wantDE := "https://de.ftp2.eu.org/d/cloudreve-upload/movie/demo.mp4"
	wantCH1 := "https://ch1.ftp2.eu.org/d/cloudreve-upload/movie/demo.mp4"

	const iterations = 100
	counts := map[string]int{}
	for range iterations {
		candidates := s.MapPathCandidates(generalURL)
		if len(candidates) != 2 {
			t.Fatalf("MapPathCandidates(%q) len = %d, want 2", generalURL, len(candidates))
		}
		if (candidates[0] != wantDE || candidates[1] != wantCH1) &&
			(candidates[0] != wantCH1 || candidates[1] != wantDE) {
			t.Fatalf("MapPathCandidates(%q) = %v, want permutation of de/ch1", generalURL, candidates)
		}
		counts[candidates[0]]++
	}

	if counts[wantDE] != 50 || counts[wantCH1] != 50 {
		t.Fatalf("equal SWRR counts = de:%d ch1:%d, want 50/50", counts[wantDE], counts[wantCH1])
	}
}

func TestStrmMapPathCandidatesEdgeCases(t *testing.T) {
	t.Run("nil 与未初始化实例安全返回原路径", func(t *testing.T) {
		raw := "https://nl.ftp2.eu.org/d/video.mkv"
		var nilStrm *Strm
		if got := nilStrm.MapPathCandidates(raw); !slices.Equal(got, []string{raw}) {
			t.Fatalf("nil.MapPathCandidates() = %v, want [%q]", got, raw)
		}
		emptyStrm := &Strm{}
		if got := emptyStrm.MapPathCandidates(raw); !slices.Equal(got, []string{raw}) {
			t.Fatalf("empty.MapPathCandidates() = %v, want [%q]", got, raw)
		}
	})

	t.Run("重复目标去重并按出现次数作为默认权重 2:1 平滑分配", func(t *testing.T) {
		s := &Strm{
			PathMap: []string{
				"//nl.ftp2.eu.org/ => //de.ftp2.eu.org/ | //de.ftp2.eu.org/ | //ch1.ftp2.eu.org/",
			},
		}
		if err := s.Init(); err != nil {
			t.Fatalf("Init() error = %v", err)
		}

		raw := "https://nl.ftp2.eu.org/d/a.mkv"
		wantDE := "https://de.ftp2.eu.org/d/a.mkv"
		wantCH1 := "https://ch1.ftp2.eu.org/d/a.mkv"
		counts := map[string]int{}
		for range 9 {
			got := s.MapPathCandidates(raw)
			if len(got) != 2 {
				t.Fatalf("MapPathCandidates() len = %d, want 2 unique candidates", len(got))
			}
			counts[got[0]]++
		}
		if counts[wantDE] != 6 || counts[wantCH1] != 3 {
			t.Fatalf("counts = de:%d ch1:%d, want 6/3", counts[wantDE], counts[wantCH1])
		}
	})
}

func TestStrmPrioritySmoothWeightedRoundRobin(t *testing.T) {
	intPtr := func(v int) *int { return &v }

	t.Run("负数 priority 校验失败", func(t *testing.T) {
		s := &Strm{
			PathMap: []string{"//nl.ftp2.eu.org/ => //de.ftp2.eu.org/ | //ch1.ftp2.eu.org/"},
			StrmProInstances: []OpenlistInstance{
				{Host: "https://de.ftp2.eu.org", Token: "t", Priority: intPtr(-1)},
			},
		}
		if err := s.Init(); err == nil {
			t.Fatal("Init() error = nil, want error for negative priority")
		}
	})

	t.Run("70/30 每 10 次严格分配 7 次 de 与 3 次 ch1 且平滑穿插", func(t *testing.T) {
		s := &Strm{
			PathMap: []string{"//nl.ftp2.eu.org/ => //de.ftp2.eu.org/ | //ch1.ftp2.eu.org/"},
			StrmProInstances: []OpenlistInstance{
				{Host: "https://de.ftp2.eu.org", Token: "t", Priority: intPtr(70)},
				{Host: "https://ch1.ftp2.eu.org", Token: "t", Priority: intPtr(30)},
			},
		}
		if err := s.Init(); err != nil {
			t.Fatalf("Init() error = %v", err)
		}

		raw := "https://nl.ftp2.eu.org/d/movie.mkv"
		wantDE := "https://de.ftp2.eu.org/d/movie.mkv"
		wantCH1 := "https://ch1.ftp2.eu.org/d/movie.mkv"

		// 验证连续 10 个周期，每个 10 次窗口内都严格满足 7:3，且 ch1 不会连续两次扎堆。
		prev := ""
		for cycle := range 10 {
			deCount, ch1Count := 0, 0
			for range 10 {
				got := s.MapPathCandidates(raw)
				if len(got) != 2 {
					t.Fatalf("candidates len = %d, want 2", len(got))
				}
				if got[0] == wantCH1 && prev == wantCH1 {
					t.Fatalf("cycle %d: ch1 was selected twice consecutively", cycle)
				}
				prev = got[0]
				switch got[0] {
				case wantDE:
					deCount++
				case wantCH1:
					ch1Count++
				default:
					t.Fatalf("unexpected target: %s", got[0])
				}
			}
			if deCount != 7 || ch1Count != 3 {
				t.Fatalf("cycle %d counts = de:%d ch1:%d, want 7/3", cycle, deCount, ch1Count)
			}
		}
	})

	t.Run("priority 为 0 时自动排除该离线节点", func(t *testing.T) {
		s := &Strm{
			PathMap: []string{"//nl.ftp2.eu.org/ => //de.ftp2.eu.org/ | //ch1.ftp2.eu.org/"},
			StrmProInstances: []OpenlistInstance{
				{Host: "https://de.ftp2.eu.org", Token: "t", Priority: intPtr(70)},
				{Host: "https://ch1.ftp2.eu.org", Token: "t", Priority: intPtr(0)},
			},
		}
		if err := s.Init(); err != nil {
			t.Fatalf("Init() error = %v", err)
		}

		raw := "https://nl.ftp2.eu.org/d/movie.mkv"
		want := []string{"https://de.ftp2.eu.org/d/movie.mkv"}
		for range 20 {
			if got := s.MapPathCandidates(raw); !slices.Equal(got, want) {
				t.Fatalf("MapPathCandidates() = %v, want %v", got, want)
			}
		}
	})

	t.Run("所有候选 priority 均为 0 时回退等权保底", func(t *testing.T) {
		s := &Strm{
			PathMap: []string{"//nl.ftp2.eu.org/ => //de.ftp2.eu.org/ | //ch1.ftp2.eu.org/"},
			StrmProInstances: []OpenlistInstance{
				{Host: "https://de.ftp2.eu.org", Token: "t", Priority: intPtr(0)},
				{Host: "https://ch1.ftp2.eu.org", Token: "t", Priority: intPtr(0)},
			},
		}
		if err := s.Init(); err != nil {
			t.Fatalf("Init() error = %v", err)
		}

		got := s.MapPathCandidates("https://nl.ftp2.eu.org/d/movie.mkv")
		if len(got) != 2 {
			t.Fatalf("MapPathCandidates() len = %d, want 2 fallback candidates", len(got))
		}
	})
}

func TestStrmStickySessionDistributionAndTTL(t *testing.T) {
	intPtr := func(v int) *int { return &v }
	now := time.Date(2026, 11, 25, 14, 0, 0, 0, time.UTC)

	s := &Strm{
		PathMap: []string{"//nl.ftp2.eu.org/ => //de.ftp2.eu.org/ | //ch1.ftp2.eu.org/"},
		StrmProInstances: []OpenlistInstance{
			{Host: "https://de.ftp2.eu.org", Token: "t", Priority: intPtr(70)},
			{Host: "https://ch1.ftp2.eu.org", Token: "t", Priority: intPtr(30)},
		},
		StickyTTL: "30m",
		nowFunc:   func() time.Time { return now },
	}
	if err := s.Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	raw := "https://nl.ftp2.eu.org/d/hot-movie.mkv"
	wantDE := "https://de.ftp2.eu.org/d/hot-movie.mkv"
	wantCH1 := "https://ch1.ftp2.eu.org/d/hot-movie.mkv"

	// 100 个不同用户同时观看同一部热门电影，按 70/30 严格分配为 70 人走 de、30 人走 ch1。
	assigned := make(map[string]string, 100)
	deUsers, ch1Users := 0, 0
	for i := range 100 {
		key := fmt.Sprintf("user-%03d|movie-1", i)
		plan := s.ResolvePathCandidates(raw, key)
		if plan.StickyHit {
			t.Fatalf("first playback for %s unexpectedly hit sticky", key)
		}
		targetURL := plan.Candidates[0].MappedURL
		assigned[key] = targetURL
		if targetURL == wantDE {
			deUsers++
		} else if targetURL == wantCH1 {
			ch1Users++
		}
	}
	if deUsers != 70 || ch1Users != 30 {
		t.Fatalf("100 users watching same movie = de:%d ch1:%d, want 70/30", deUsers, ch1Users)
	}

	// 某个用户拖动进度条 20 次（并在 20 分钟后再次拖动），必须始终固定在原节点，且滑动续期生效。
	sampleKey := "user-001|movie-1" // 第 2 个用户按 SWRR 应分配在 ch1
	if assigned[sampleKey] != wantCH1 {
		t.Fatalf("sampleKey assigned = %s, want %s", assigned[sampleKey], wantCH1)
	}
	now = now.Add(20 * time.Minute)
	for range 20 {
		plan := s.ResolvePathCandidates(raw, sampleKey)
		if !plan.StickyHit {
			t.Fatal("expected sticky hit during seek")
		}
		if plan.Candidates[0].MappedURL != wantCH1 {
			t.Fatalf("seek switched node to %s, want %s", plan.Candidates[0].MappedURL, wantCH1)
		}
	}

	// 再推进 20 分钟（距首次 40 分钟，但距上次拖动仅 20 分钟），滑动有效期仍应保持命中。
	now = now.Add(20 * time.Minute)
	if plan := s.ResolvePathCandidates(raw, sampleKey); !plan.StickyHit || plan.Candidates[0].MappedURL != wantCH1 {
		t.Fatalf("sliding TTL failed: hit=%v url=%s", plan.StickyHit, plan.Candidates[0].MappedURL)
	}

	// 推进 31 分钟无请求后粘性过期，下次应重新走 SWRR 分配（此时恰好处于周期首位，会落到 de）。
	now = now.Add(31 * time.Minute)
	plan := s.ResolvePathCandidates(raw, sampleKey)
	if plan.StickyHit {
		t.Fatal("expected sticky miss after TTL expired")
	}
	if plan.Candidates[0].MappedURL != wantDE {
		t.Fatalf("after TTL expiry got %s, want %s (first slot of fresh SWRR cycle)", plan.Candidates[0].MappedURL, wantDE)
	}
}
