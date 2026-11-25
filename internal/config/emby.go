package config

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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

// StickyScope 定义 strm 负载均衡会话粘性的生效维度。
type StickyScope string

const (
	// StickyScopeUserItem 按“用户 + 影片”做粘性，同一用户看同一部片期间固定节点，换片后重新按权重分配。
	StickyScopeUserItem StickyScope = "user-item"
	// StickyScopeUser 按“用户”做粘性，同一用户在有效期内固定节点。
	StickyScopeUser StickyScope = "user"

	// defaultStrmStickyTTL 会话粘性默认滑动有效期。
	defaultStrmStickyTTL = 30 * time.Minute
	// stickyCleanupInterval 清理过期粘性记录的最小时间间隔。
	stickyCleanupInterval = 5 * time.Minute
	// stickyMaxEntries 粘性缓存条目数量上限，防止异常流量导致内存无限增长。
	stickyMaxEntries = 100000
)

// validStickyScope 用于校验用户配置的会话粘性维度。
var validStickyScope = map[StickyScope]struct{}{
	StickyScopeUserItem: {},
	StickyScopeUser:     {},
}

// strmPathRule 描述单条远程 strm 路径映射规则，支持多个候选目标并保存平滑加权轮询状态。
type strmPathRule struct {
	from           string
	targets        []string
	currentWeights map[string]int64
}

// StrmPathCandidate 描述单条路径映射后的候选地址及其目标片段和有效权重。
type StrmPathCandidate struct {
	Target    string
	MappedURL string
	Weight    int64
}

// StrmPathPlan 描述单次 strm 路径解析的规则命中情况与有序候选列表。
type StrmPathPlan struct {
	RuleFrom    string
	Matched     bool
	StickyHit   bool
	TotalWeight int64
	Candidates  []StrmPathCandidate
}

// stickyEntry 保存单条会话粘性映射及其过期时间。
type stickyEntry struct {
	target    string
	expiresAt time.Time
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
	// StickyTTL 会话粘性滑动有效期（支持 s/m/h/d，默认 30m；设为 0 或 0s 可关闭粘性）。
	StickyTTL string `yaml:"sticky-ttl"`
	// StickyScope 会话粘性维度，支持 user-item（默认）和 user。
	StickyScope StickyScope `yaml:"sticky-scope"`

	// pathMap 初始化并归并后的路径映射规则，按首次出现的顺序自上而下匹配。
	pathMap []strmPathRule
	// stickyTTL 解析后的会话粘性时长；为 0 表示关闭粘性。
	stickyTTL time.Duration
	// lbMu 保护平滑加权轮询状态与会话粘性表的并发读写。
	lbMu sync.Mutex
	// stickyEntries 保存 "ruleFrom\x00stickyKey" 到目标节点的粘性映射。
	stickyEntries map[string]stickyEntry
	// nextStickyCleanup 下次全量清理过期粘性记录的时间点。
	nextStickyCleanup time.Time
	// nowFunc 返回当前时间，为空时使用 time.Now，便于单元测试控制时间推进。
	nowFunc func() time.Time
}

// Init 初始化并校验 strm 路径映射规则、实例权重以及会话粘性配置。
// 支持在 "=>" 右侧使用 "|" 配置多个候选目标，也支持多行重复同一源片段自动合并，
// 合并后保持该源片段首次出现时的匹配优先级。
func (s *Strm) Init() error {
	for i, instance := range s.StrmProInstances {
		if instance.Priority != nil && *instance.Priority < 0 {
			return fmt.Errorf("strmpro-instances[%d] (%s) 的 priority 配置错误: %d, 不能小于 0", i, instance.Host, *instance.Priority)
		}
	}

	stickyTTL, err := parseStrmStickyTTL(s.StickyTTL)
	if err != nil {
		return err
	}
	s.stickyTTL = stickyTTL

	scope := StickyScope(strings.TrimSpace(string(s.StickyScope)))
	if scope == "" {
		scope = StickyScopeUserItem
	}
	if _, ok := validStickyScope[scope]; !ok {
		return fmt.Errorf("sticky-scope 配置错误: %s, 有效值: %v", scope, maps.Keys(validStickyScope))
	}
	s.StickyScope = scope

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
			from:           from,
			targets:        targets,
			currentWeights: make(map[string]int64, len(targets)),
		})
	}

	s.stickyEntries = make(map[string]stickyEntry)
	s.nextStickyCleanup = time.Time{}
	return nil
}

// parseStrmStickyTTL 解析会话粘性有效期配置。
// 空字符串使用默认值 30m；"0" 或零时长表示禁用粘性；支持 "d" 天后缀及标准 Go 时长格式。
func parseStrmStickyTTL(raw string) (time.Duration, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return defaultStrmStickyTTL, nil
	}
	if trimmed == "0" {
		return 0, nil
	}
	if strings.HasSuffix(trimmed, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(trimmed, "d"))
		if err != nil || days < 0 {
			return 0, fmt.Errorf("sticky-ttl 配置错误: %s, 需为非负时长（如 30m、1h、1d 或 0）", raw)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}

	d, err := time.ParseDuration(trimmed)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("sticky-ttl 配置错误: %s, 需为非负时长（如 30m、1h、1d 或 0）", raw)
	}
	return d, nil
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
// 当命中规则存在多个候选目标时，按平滑加权轮询算法选取本次目标进行替换。
func (s *Strm) MapPath(path string) string {
	candidates := s.MapPathCandidates(path)
	return candidates[0]
}

// MapPathCandidates 返回传入路径命中首条规则后的候选映射地址列表（不启用会话粘性）。
func (s *Strm) MapPathCandidates(path string) []string {
	plan := s.ResolvePathCandidates(path, "")
	urls := make([]string, 0, len(plan.Candidates))
	for _, candidate := range plan.Candidates {
		urls = append(urls, candidate.MappedURL)
	}
	return urls
}

// ResolvePathCandidates 按规则顺序解析候选映射地址。
// 当命中多目标规则时：
//  1. 若传入了 stickyKey 且会话粘性已启用并命中有效记录，则优先固定返回已粘滞的节点且不推进轮询计数；
//  2. 否则按平滑加权轮询（Nginx SWRR）根据实例 priority 权重严格按比例平滑选出主节点，并预写入粘性记录；
//  3. 其余有效候选节点按权重降序排在主节点之后，供调用方在节点异常时顺序回退。
func (s *Strm) ResolvePathCandidates(path, stickyKey string) StrmPathPlan {
	if s == nil {
		return StrmPathPlan{
			Candidates: []StrmPathCandidate{{MappedURL: path}},
		}
	}

	for i := range s.pathMap {
		rule := &s.pathMap[i]
		if !strings.Contains(path, rule.from) {
			continue
		}

		active := s.buildWeightedCandidates(path, *rule)
		if len(rule.targets) == 1 {
			logs.Tip("映射路径: [%s] => [%s]", rule.from, active[0].Target)
			return StrmPathPlan{
				RuleFrom:    rule.from,
				Matched:     true,
				TotalWeight: active[0].Weight,
				Candidates:  active,
			}
		}

		ordered, totalWeight, stickyHit := s.selectMultiTargetCandidates(rule, active, stickyKey)
		if stickyHit {
			logs.Tip("映射路径(会话粘性): [%s] => 固定 [%s] (候选数: %d)", rule.from, ordered[0].Target, len(ordered))
		} else {
			logs.Tip("映射路径(平滑负载均衡): [%s] => 优先 [%s] (权重: %d/%d, 候选数: %d)", rule.from, ordered[0].Target, ordered[0].Weight, totalWeight, len(ordered))
		}

		return StrmPathPlan{
			RuleFrom:    rule.from,
			Matched:     true,
			StickyHit:   stickyHit,
			TotalWeight: totalWeight,
			Candidates:  ordered,
		}
	}

	return StrmPathPlan{
		Candidates: []StrmPathCandidate{{MappedURL: path}},
	}
}

// UpdateStickyTarget 在故障回退切到健康节点后，更新对应会话的粘性目标并刷新过期时间。
func (s *Strm) UpdateStickyTarget(ruleFrom, stickyKey, target string) {
	if s == nil || s.stickyTTL <= 0 || ruleFrom == "" || stickyKey == "" || target == "" {
		return
	}

	s.lbMu.Lock()
	defer s.lbMu.Unlock()

	now := s.now()
	s.cleanupStickyLocked(now)
	s.setStickyLocked(composeStickyKey(ruleFrom, stickyKey), target, now)
}

// ClearStickyTarget 在所有候选节点均解析失败时移除对应会话的粘性记录，避免下次继续粘滞不可用节点。
func (s *Strm) ClearStickyTarget(ruleFrom, stickyKey string) {
	if s == nil || ruleFrom == "" || stickyKey == "" {
		return
	}

	s.lbMu.Lock()
	defer s.lbMu.Unlock()

	if s.stickyEntries != nil {
		delete(s.stickyEntries, composeStickyKey(ruleFrom, stickyKey))
	}
}

// selectMultiTargetCandidates 在锁内完成会话粘性查找、平滑加权轮询选路及粘性更新。
func (s *Strm) selectMultiTargetCandidates(rule *strmPathRule, active []StrmPathCandidate, stickyKey string) ([]StrmPathCandidate, int64, bool) {
	var totalWeight int64
	for _, candidate := range active {
		totalWeight += candidate.Weight
	}

	s.lbMu.Lock()
	defer s.lbMu.Unlock()

	now := s.now()
	useSticky := s.stickyTTL > 0 && stickyKey != ""
	fullStickyKey := ""
	if useSticky {
		s.cleanupStickyLocked(now)
		fullStickyKey = composeStickyKey(rule.from, stickyKey)
		if entry, ok := s.stickyEntries[fullStickyKey]; ok && now.Before(entry.expiresAt) {
			if pinnedIdx := findCandidateIndex(active, entry.target); pinnedIdx >= 0 {
				// 命中有效会话粘性时刷新滑动过期时间，不推进 SWRR 权重，避免同一播放多次拖动进度条影响其他用户比例。
				entry.expiresAt = now.Add(s.stickyTTL)
				s.stickyEntries[fullStickyKey] = entry
				return orderCandidatesWithPrimary(active, pinnedIdx), totalWeight, true
			}
			// 原粘性节点已被停用或移出候选列表，清理旧记录并走平滑加权轮询重选。
			delete(s.stickyEntries, fullStickyKey)
		}
	}

	primaryIdx := pickSmoothWeightedIndex(rule, active, totalWeight)
	ordered := orderCandidatesWithPrimary(active, primaryIdx)
	if useSticky {
		// 预先记录本次选中的主节点，确保同一播放器并发发起多个初始分段请求时立即落到同一节点。
		s.setStickyLocked(fullStickyKey, ordered[0].Target, now)
	}
	return ordered, totalWeight, false
}

// pickSmoothWeightedIndex 执行 Nginx 平滑加权轮询（SWRR）算法，返回本次应选中的候选索引。
// 每次调用将所有活跃候选的当前权重加上各自有效权重，选出当前权重最大者并将其当前权重减去总权重，
// 从而在每个权重周期内严格按比例且均匀穿插分配请求。
func pickSmoothWeightedIndex(rule *strmPathRule, active []StrmPathCandidate, totalWeight int64) int {
	if len(active) <= 1 {
		return 0
	}
	if rule.currentWeights == nil {
		rule.currentWeights = make(map[string]int64, len(active))
	}

	activeSet := make(map[string]struct{}, len(active))
	bestIdx := 0
	var bestWeight int64
	for i, candidate := range active {
		activeSet[candidate.Target] = struct{}{}
		cw := rule.currentWeights[candidate.Target] + candidate.Weight
		rule.currentWeights[candidate.Target] = cw
		if i == 0 || cw > bestWeight {
			bestIdx = i
			bestWeight = cw
		}
	}

	// 清理已下线（如 priority 设为 0）节点的遗留权重，避免恢复上线时出现历史累计偏移。
	for target := range rule.currentWeights {
		if _, ok := activeSet[target]; !ok {
			delete(rule.currentWeights, target)
		}
	}

	rule.currentWeights[active[bestIdx].Target] -= totalWeight
	return bestIdx
}

// orderCandidatesWithPrimary 将主候选置于首位，其余候选按有效权重降序排列作为故障回退顺序。
func orderCandidatesWithPrimary(active []StrmPathCandidate, primaryIdx int) []StrmPathCandidate {
	ordered := make([]StrmPathCandidate, 0, len(active))
	ordered = append(ordered, active[primaryIdx])

	fallbacks := make([]StrmPathCandidate, 0, len(active)-1)
	for i, candidate := range active {
		if i == primaryIdx {
			continue
		}
		fallbacks = append(fallbacks, candidate)
	}
	sort.SliceStable(fallbacks, func(i, j int) bool {
		return fallbacks[i].Weight > fallbacks[j].Weight
	})
	ordered = append(ordered, fallbacks...)
	return ordered
}

// buildWeightedCandidates 对规则中的候选目标去重并结合 StrmProInstances 的 priority 计算有效权重。
// 当存在至少一个正权重候选时，自动过滤掉权重为 0 的节点；若全部候选权重均为 0，则回退为等权保底。
func (s *Strm) buildWeightedCandidates(path string, rule strmPathRule) []StrmPathCandidate {
	unique := make([]StrmPathCandidate, 0, len(rule.targets))
	indexByTarget := make(map[string]int, len(rule.targets))
	counts := make([]int64, 0, len(rule.targets))

	for _, target := range rule.targets {
		if idx, exists := indexByTarget[target]; exists {
			counts[idx]++
			continue
		}
		indexByTarget[target] = len(unique)
		unique = append(unique, StrmPathCandidate{
			Target:    target,
			MappedURL: strings.Replace(path, rule.from, target, 1),
		})
		counts = append(counts, 1)
	}

	positive := make([]StrmPathCandidate, 0, len(unique))
	for i := range unique {
		unique[i].Weight = s.resolveCandidateWeight(unique[i].MappedURL, counts[i])
		if unique[i].Weight > 0 {
			positive = append(positive, unique[i])
		}
	}
	if len(positive) > 0 {
		return positive
	}

	// 兜底：若规则下所有目标的 priority 均被设为 0，仍按等权保留候选避免请求完全无路可走。
	for i := range unique {
		unique[i].Weight = 1
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

// setStickyLocked 写入或更新粘性映射（调用方需已持有 lbMu）。
func (s *Strm) setStickyLocked(key, target string, now time.Time) {
	if s.stickyEntries == nil {
		s.stickyEntries = make(map[string]stickyEntry)
	}
	if _, exists := s.stickyEntries[key]; !exists && len(s.stickyEntries) >= stickyMaxEntries {
		// 达到容量上限时强制清理过期条目；若仍满则淘汰最早过期的一条。
		s.sweepExpiredStickyLocked(now)
		if len(s.stickyEntries) >= stickyMaxEntries {
			evictEarliestStickyLocked(s.stickyEntries)
		}
	}
	s.stickyEntries[key] = stickyEntry{
		target:    target,
		expiresAt: now.Add(s.stickyTTL),
	}
}

// cleanupStickyLocked 按时间间隔惰性清理过期的粘性记录（调用方需已持有 lbMu）。
func (s *Strm) cleanupStickyLocked(now time.Time) {
	if len(s.stickyEntries) == 0 {
		return
	}
	if !s.nextStickyCleanup.IsZero() && now.Before(s.nextStickyCleanup) {
		return
	}
	s.sweepExpiredStickyLocked(now)
	s.nextStickyCleanup = now.Add(stickyCleanupInterval)
}

// sweepExpiredStickyLocked 删除所有已过期的粘性条目（调用方需已持有 lbMu）。
func (s *Strm) sweepExpiredStickyLocked(now time.Time) {
	for k, entry := range s.stickyEntries {
		if !now.Before(entry.expiresAt) {
			delete(s.stickyEntries, k)
		}
	}
}

// evictEarliestStickyLocked 淘汰最快过期的一条粘性记录，防止极端情况下内存失控。
func evictEarliestStickyLocked(entries map[string]stickyEntry) {
	var victimKey string
	var earliest time.Time
	first := true
	for k, entry := range entries {
		if first || entry.expiresAt.Before(earliest) {
			victimKey = k
			earliest = entry.expiresAt
			first = false
		}
	}
	if !first {
		delete(entries, victimKey)
	}
}

// findCandidateIndex 查找指定目标片段在候选列表中的位置，未找到返回 -1。
func findCandidateIndex(candidates []StrmPathCandidate, target string) int {
	for i, candidate := range candidates {
		if candidate.Target == target {
			return i
		}
	}
	return -1
}

// composeStickyKey 组合规则源片段与会话标识，避免不同映射规则的粘性记录互相覆盖。
func composeStickyKey(ruleFrom, stickyKey string) string {
	return ruleFrom + "\x00" + stickyKey
}

// now 返回当前时间。
func (s *Strm) now() time.Time {
	if s != nil && s.nowFunc != nil {
		return s.nowFunc()
	}
	return time.Now()
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
