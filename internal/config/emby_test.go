package config

import (
	"slices"
	"testing"
)

func TestStrmInit(t *testing.T) {
	tests := []struct {
		name      string
		pathMap   []string
		wantRules []strmPathRule
		wantErr   bool
	}{
		{
			name: "单目标与多目标规则混合",
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Strm{PathMap: tt.pathMap}
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
	for range 50 {
		candidates := s.MapPathCandidates(mplocalURL)
		if !slices.Equal(candidates, []string{wantMplocal}) {
			t.Fatalf("MapPathCandidates(%q) = %v, want [%q]", mplocalURL, candidates, wantMplocal)
		}
	}

	// 其余 nl 路径应在 de 与 ch1 之间随机均衡分布，且每次都返回两个不重复候选。
	generalURL := "https://nl.ftp2.eu.org/d/cloudreve-upload/movie/demo.mp4"
	wantDE := "https://de.ftp2.eu.org/d/cloudreve-upload/movie/demo.mp4"
	wantCH1 := "https://ch1.ftp2.eu.org/d/cloudreve-upload/movie/demo.mp4"

	const iterations = 1000
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

	if counts[wantDE] < 350 || counts[wantCH1] < 350 {
		t.Fatalf("load balance skewed: de=%d, ch1=%d out of %d", counts[wantDE], counts[wantCH1], iterations)
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

	t.Run("重复目标在洗牌后去重避免重复回退同一节点", func(t *testing.T) {
		s := &Strm{
			PathMap: []string{
				"//nl.ftp2.eu.org/ => //de.ftp2.eu.org/ | //de.ftp2.eu.org/ | //ch1.ftp2.eu.org/",
			},
			// 固定返回最大值索引，使洗牌保持原序以验证去重结果确定性。
			randIntN: func(n int) int { return n - 1 },
		}
		if err := s.Init(); err != nil {
			t.Fatalf("Init() error = %v", err)
		}

		got := s.MapPathCandidates("https://nl.ftp2.eu.org/d/a.mkv")
		want := []string{
			"https://de.ftp2.eu.org/d/a.mkv",
			"https://ch1.ftp2.eu.org/d/a.mkv",
		}
		if !slices.Equal(got, want) {
			t.Fatalf("MapPathCandidates() = %v, want %v", got, want)
		}
	})
}
