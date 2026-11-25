package config

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"

	"github.com/AmbitiousJun/go-emby2openlist/v2/internal/util/logs"
	"github.com/AmbitiousJun/go-emby2openlist/v2/internal/util/maps"
	"github.com/AmbitiousJun/go-emby2openlist/v2/internal/util/strs"
)

// PeStrategy 代理异常策略类型
type PeStrategy string

const (
	PeStrategyOrigin PeStrategy = "origin" // 回源
	PeStrategyReject PeStrategy = "reject" // 拒绝请求
)

// DlStrategy 下载策略类型
type DlStrategy string

const (
	DlStrategyOrigin DlStrategy = "origin" // 代理到源服务器
	DlStrategyDirect DlStrategy = "direct" // 获取并重定向到直链
	DlStrategy403    DlStrategy = "403"    // 拒绝响应
)

// validPeStrategy 用于校验用户配置的策略是否合法
var validPeStrategy = map[PeStrategy]struct{}{
	PeStrategyOrigin: {}, PeStrategyReject: {},
}

// validDlStrategy 用于校验用户配置的下载策略是否合法
var validDlStrategy = map[DlStrategy]struct{}{
	DlStrategyOrigin: {}, DlStrategyDirect: {}, DlStrategy403: {},
}

// Emby 相关配置
type Emby struct {
	// Emby 源服务器地址
	Host string `yaml:"host"`
	// rclone 或者 cd 的挂载目录
	MountPath string `yaml:"mount-path"`
	// EpisodesUnplayPrior 在获取剧集列表时是否将未播资源优先展示
	EpisodesUnplayPrior bool `yaml:"episodes-unplay-prior"`
	// ResortRandomItems 是否对随机的 items 进行重排序
	ResortRandomItems bool `yaml:"resort-random-items"`
	// ProxyErrorStrategy 代理错误时的处理策略
	ProxyErrorStrategy PeStrategy `yaml:"proxy-error-strategy"`
	// ImagesQuality 图片质量
	ImagesQuality int `yaml:"images-quality"`
	// Strm strm 配置
	Strm *Strm `yaml:"strm"`
	// DownloadStrategy 下载接口响应策略
	DownloadStrategy DlStrategy `yaml:"download-strategy"`
	// LocalMediaRoots 本地媒体根路径
	LocalMediaRoots []string `yaml:"local-media-roots"`
	// CustomCssJs 自定义 css js 配置
	CustomCssJs *CustomCssJs `yaml:"custom-css-js"`
}

func (e *Emby) Init() error {
	if strs.AnyEmpty(e.Host) {
		return errors.New("emby.host 配置不能为空")
	}
	if strs.AnyEmpty(string(e.ProxyErrorStrategy)) {
		// 失败默认回源
		e.ProxyErrorStrategy = PeStrategyOrigin
	}
	if strs.AnyEmpty(string(e.DownloadStrategy)) {
		// 默认响应直链
		e.DownloadStrategy = DlStrategyDirect
	}

	e.ProxyErrorStrategy = PeStrategy(strings.TrimSpace(string(e.ProxyErrorStrategy)))
	if _, ok := validPeStrategy[e.ProxyErrorStrategy]; !ok {
		return fmt.Errorf("emby.proxy-error-strategy 配置错误, 有效值: %v", maps.Keys(validPeStrategy))
	}

	if e.ImagesQuality == 0 {
		// 不允许配置零值
		e.ImagesQuality = 70
	}
	if e.ImagesQuality < 0 || e.ImagesQuality > 100 {
		return fmt.Errorf("emby.images-quality 配置错误: %d, 允许配置范围: [1, 100]", e.ImagesQuality)
	}

	if e.Strm == nil {
		e.Strm = new(Strm)
	}
	if err := e.Strm.Init(); err != nil {
		return fmt.Errorf("emby.strm 配置错误: %v", err)
	}

	e.DownloadStrategy = DlStrategy(strings.TrimSpace(string(e.DownloadStrategy)))
	if _, ok := validDlStrategy[e.DownloadStrategy]; !ok {
		return fmt.Errorf("emby.download-strategy 配置错误, 有效值: %v", maps.Keys(validDlStrategy))
	}

	if e.CustomCssJs == nil {
		e.CustomCssJs = new(CustomCssJs)
	}
	if err := e.CustomCssJs.Init(); err != nil {
		return fmt.Errorf("emby.custom-css-js 配置错误: %v", err)
	}

	return nil
}

// IsLocalMediaPath 判断路径是否为本地媒体路径
func (e *Emby) IsLocalMediaPath(p string) bool {
	// 根据配置文件中的本地文件进行判定
	for _, root := range e.LocalMediaRoots {
		if strings.HasPrefix(p, root) {
			return true
		}
	}

	// 根据路径前缀判定是否是网络共享
	sharedPrefixes := []string{"smb://", "//"}
	for _, prefix := range sharedPrefixes {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}

	return false
}

// strmPathRule 描述单条远程 strm 路径映射规则，支持多个候选目标用于负载均衡。
type strmPathRule struct {
	from    string
	targets []string
}

// weightedPathCandidate 描述单个去重后的映射候选地址及其分配权重。
type weightedPathCandidate struct {
	target    string
	mappedURL string
	weight    int64
}

// Strm 保存远程 strm 路径映射与 StrmPro 配置。
type Strm struct {
	// PathMap 远程路径映射规则列表。
	PathMap []string `yaml:"path-map"`
	// StrmPro 是否通过 Openlist API 将 strm 地址刷新为实时直链。
	StrmPro bool `yaml:"strmpro"`
	// StrmProInstances StrmPro 按地址主机名匹配的 Openlist 实例列表。
	StrmProInstances []OpenlistInstance `yaml:"strmpro-instances"`
	// InternalRedirectEnable 是否启用 strm 内部重定向。
	InternalRedirectEnable bool `yaml:"internal-redirect-enable"`

	// pathMap 初始化并归并后的路径映射规则，按首次出现的顺序自上而下匹配。
	pathMap []strmPathRule
	// randIntN 返回 [0, n) 内的随机整数；为空时使用标准库并发安全的 math/rand/v2.Int64N。
	randIntN func(n int64) int64
}

// Init 初始化并校验 strm 路径映射规则及 StrmPro 实例权重配置。
// 支持在 "=>" 右侧使用 "|" 配置多个候选目标，也支持多行重复同一源片段自动合并，
// 合并后保持该源片段首次出现时的匹配优先级。
func (s *Strm) Init() error {
	for i, instance := range s.StrmProInstances {
		if instance.Priority != nil && *instance.Priority < 0 {
			return fmt.Errorf("strmpro-instances[%d] (%s) 的 priority 配置错误: %d, 不能小于 0", i, instance.Host, *instance.Priority)
		}
	}

	s.pathMap = make([]strmPathRule, 0, len(s.PathMap))
	ruleIndex := make(map[string]int, len(s.PathMap))

	for _, rawRule := range s.PathMap {
		splits := strings.Split(rawRule, "=>")
		if len(splits) != 2 {
			return fmt.Errorf("映射配置不规范: %s, 请使用 => 进行分割", rawRule)
		}

		from := strings.TrimSpace(splits[0])
		if from == "" {
			return fmt.Errorf("映射配置不规范: %s, 源路径片段不能为空", rawRule)
		}

		targets, err := parseStrmPathTargets(rawRule, splits[1])
		if err != nil {
			return err
		}

		if idx, exists := ruleIndex[from]; exists {
			s.pathMap[idx].targets = append(s.pathMap[idx].targets, targets...)
			continue
		}

		ruleIndex[from] = len(s.pathMap)
		s.pathMap = append(s.pathMap, strmPathRule{
			from:    from,
			targets: targets,
		})
	}
	return nil
}

// parseStrmPathTargets 解析单条规则中 "=>" 右侧的目标列表。
// 当不含 "|" 时保留单个目标（允许空串用于移除前缀）；含 "|" 时要求每个候选目标均非空。
func parseStrmPathTargets(rawRule, rawTarget string) ([]string, error) {
	if !strings.Contains(rawTarget, "|") {
		return []string{strings.TrimSpace(rawTarget)}, nil
	}

	parts := strings.Split(rawTarget, "|")
	targets := make([]string, 0, len(parts))
	for _, part := range parts {
		target := strings.TrimSpace(part)
		if target == "" {
			return nil, fmt.Errorf("映射配置不规范: %s, 多目标映射中存在空目标", rawRule)
		}
		targets = append(targets, target)
	}
	return targets, nil
}

// MapPath 将传入路径按规则顺序进行首次命中映射。
// 当命中规则存在多个候选目标时，按实例配置的 priority 权重随机选取一个目标进行替换。
func (s *Strm) MapPath(path string) string {
	candidates := s.MapPathCandidates(path)
	return candidates[0]
}

// MapPathCandidates 返回传入路径命中首条规则后的候选映射结果列表。
// 多目标规则会结合对应 Openlist 实例的 priority 权重做无放回加权随机排序：
// 例如 de 配置 70、ch1 配置 30 时，首选中 de 的概率为 70%、ch1 为 30%，
// 若首选节点异常则按剩余权重顺序回退；未配置 priority 时默认权重为 1；
// priority 为 0 的节点在存在其他正权重节点时会被排除出候选列表。
// 未命中任何规则或配置为空时，返回仅包含原路径的单元素切片。
func (s *Strm) MapPathCandidates(path string) []string {
	if s == nil {
		return []string{path}
	}

	for _, rule := range s.pathMap {
		if !strings.Contains(path, rule.from) {
			continue
		}

		weighted := s.buildWeightedCandidates(path, rule)
		ordered, totalWeight := s.orderWeightedCandidates(weighted)
		if len(rule.targets) == 1 {
			logs.Tip("映射路径: [%s] => [%s]", rule.from, ordered[0].target)
		} else {
			logs.Tip("映射路径(负载均衡): [%s] => 优先 [%s] (权重: %d/%d, 候选数: %d)", rule.from, ordered[0].target, ordered[0].weight, totalWeight, len(ordered))
		}

		candidates := make([]string, 0, len(ordered))
		for _, candidate := range ordered {
			candidates = append(candidates, candidate.mappedURL)
		}
		return candidates
	}
	return []string{path}
}

// buildWeightedCandidates 对规则中的候选目标去重并结合 StrmProInstances 的 priority 计算最终权重。
// 当存在至少一个正权重候选时，自动过滤掉权重为 0 的节点；若全部候选权重均为 0，则回退为等权保底。
func (s *Strm) buildWeightedCandidates(path string, rule strmPathRule) []weightedPathCandidate {
	unique := make([]weightedPathCandidate, 0, len(rule.targets))
	indexByTarget := make(map[string]int, len(rule.targets))
	counts := make([]int64, 0, len(rule.targets))

	for _, target := range rule.targets {
		if idx, exists := indexByTarget[target]; exists {
			counts[idx]++
			continue
		}
		indexByTarget[target] = len(unique)
		unique = append(unique, weightedPathCandidate{
			target:    target,
			mappedURL: strings.Replace(path, rule.from, target, 1),
		})
		counts = append(counts, 1)
	}

	positive := make([]weightedPathCandidate, 0, len(unique))
	for i := range unique {
		unique[i].weight = s.resolveCandidateWeight(unique[i].mappedURL, counts[i])
		if unique[i].weight > 0 {
			positive = append(positive, unique[i])
		}
	}
	if len(positive) > 0 {
		return positive
	}

	// 兜底：若规则下所有目标的 priority 均被设为 0，仍按等权保留候选避免请求完全无路可走。
	for i := range unique {
		unique[i].weight = 1
	}
	return unique
}

// resolveCandidateWeight 查询候选地址匹配实例的 priority 配置。
// 若匹配到配置了 priority 的实例则返回该权重，否则回退为该目标在 path-map 中的出现次数（默认 1）。
func (s *Strm) resolveCandidateWeight(mappedURL string, defaultWeight int64) int64 {
	if len(s.StrmProInstances) == 0 {
		return defaultWeight
	}
	instance, matched, err := MatchOpenlistInstance(mappedURL, s.StrmProInstances)
	if err != nil || !matched || instance.Priority == nil {
		return defaultWeight
	}
	if *instance.Priority <= 0 {
		return 0
	}
	return int64(*instance.Priority)
}

// orderWeightedCandidates 按候选权重执行无放回加权随机抽样，返回排序后的候选列表及初始总权重。
func (s *Strm) orderWeightedCandidates(candidates []weightedPathCandidate) ([]weightedPathCandidate, int64) {
	if len(candidates) <= 1 {
		var total int64
		if len(candidates) == 1 {
			total = candidates[0].weight
		}
		return candidates, total
	}

	remaining := append([]weightedPathCandidate(nil), candidates...)
	var totalWeight int64
	for _, candidate := range remaining {
		totalWeight += candidate.weight
	}
	initialTotal := totalWeight

	ordered := make([]weightedPathCandidate, 0, len(remaining))
	for len(remaining) > 0 {
		if len(remaining) == 1 {
			ordered = append(ordered, remaining[0])
			break
		}

		pick := s.int64N(totalWeight)
		var acc int64
		chosenIdx := len(remaining) - 1
		for i, candidate := range remaining {
			acc += candidate.weight
			if pick < acc {
				chosenIdx = i
				break
			}
		}

		chosen := remaining[chosenIdx]
		ordered = append(ordered, chosen)
		totalWeight -= chosen.weight
		remaining = append(remaining[:chosenIdx], remaining[chosenIdx+1:]...)
	}
	return ordered, initialTotal
}

// int64N 返回 [0, n) 范围内的随机整数。
func (s *Strm) int64N(n int64) int64 {
	if s != nil && s.randIntN != nil {
		return s.randIntN(n)
	}
	return rand.Int64N(n)
}

// CustomCssJs 自定义 css js 配置
type CustomCssJs struct {

	// DebugMode 调试模式开关
	DebugMode bool `yaml:"debug-mode"`
}

// Init 配置初始化
func (c *CustomCssJs) Init() error {
	return nil
}
