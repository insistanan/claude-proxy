package sensitive

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
)

// InfoMatch 描述一次敏感信息命中及其掩码结果。
type InfoMatch struct {
	Rule        string `json:"rule"`
	Original    string `json:"original"`
	Replacement string `json:"replacement"`
	Start       int    `json:"start"`
	End         int    `json:"end"`
}

type infoRule struct {
	name  string
	regex *regexp.Regexp
	find  func(string) []infoCandidate
	mask  func(string) string
}

type infoCandidate struct {
	start       int
	end         int
	replacement string
}

var (
	idCardPattern   = regexp.MustCompile(`\b[1-9][0-9]{16}[0-9Xx]\b`)
	ipv4Pattern     = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)
	ipv6Pattern     = regexp.MustCompile(`(?i)[0-9a-f:][0-9a-f:.]*:[0-9a-f:.]*`)
	bankCardPattern = regexp.MustCompile(`(?:^|[^0-9])([0-9](?:[ -]?[0-9]){12,18})(?:[^0-9]|$)`)

	// versionHintPattern 匹配 IPv4 命中前文末尾的版本号语境词
	//（version/release/build/rev/revision，允许字母数字与 _-. 前缀，
	// 例如 appVersion、my-build），命中则按版本号放行。
	// RE2 不支持 lookbehind，因此对命中位置的前文窗口做锚定结尾匹配。
	versionHintPattern = regexp.MustCompile(`(?i)(?:^|[^0-9a-z.])[a-z0-9_.-]*(?:version|release|build|rev(?:ision)?)[\s:@=_-]*$`)

	// versionHintWindow 是版本号启发向前回看的字符窗口。
	versionHintWindow = 32
)

// extraNonPublicIPNets 是标准库判定方法覆盖不到、public 掩码范围下同样放行的
// 保留网段：本网段、运营商级 NAT、IETF 协议分配、文档示例、基准测试、6to4
// 中继、保留段与 IPv6 专用段。这些地址无个人定位价值，出现在代码与配置里是常态。
var extraNonPublicIPNets = mustParseCIDRs(
	"0.0.0.0/8",       // 本网段（RFC 791）
	"100.64.0.0/10",   // 运营商级 NAT（RFC 6598）
	"192.0.0.0/24",    // IETF 协议分配（RFC 6890）
	"192.0.2.0/24",    // 文档示例 TEST-NET-1（RFC 5737）
	"192.88.99.0/24",  // 6to4 中继（RFC 7526 已废弃）
	"198.18.0.0/15",   // 基准测试（RFC 2544）
	"198.51.100.0/24", // 文档示例 TEST-NET-2（RFC 5737）
	"203.0.113.0/24",  // 文档示例 TEST-NET-3（RFC 5737）
	"240.0.0.0/4",     // 保留段（RFC 1112；含受限广播地址）
	"2001:db8::/32",   // IPv6 文档示例（RFC 3849）
	"64:ff9b:1::/48",  // 本地 NAT64 翻译示例（RFC 8215）
	"100::/64",        // 丢弃前缀（RFC 6666）
)

func mustParseCIDRs(cidrs ...string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Sprintf("解析保留网段 %s 失败: %v", cidr, err))
		}
		nets = append(nets, ipNet)
	}
	return nets
}

// InfoDetector 检测并掩码请求方向的敏感信息。
type InfoDetector struct {
	enabled bool
	rules   []infoRule
}

// NewInfoDetector 根据设置创建敏感信息检测器。
func NewInfoDetector(settings config.SensitiveInfoConfig) (*InfoDetector, error) {
	if err := validateInfoRules(settings.EnabledRules); err != nil {
		return nil, err
	}
	ipMaskScope, err := normalizeIPMaskScope(settings.IPMaskScope)
	if err != nil {
		return nil, err
	}
	detector := &InfoDetector{enabled: settings.Enabled}
	if !settings.Enabled {
		return detector, nil
	}

	selected := make(map[string]struct{}, len(settings.EnabledRules))
	for _, rule := range settings.EnabledRules {
		selected[rule] = struct{}{}
	}
	for _, rule := range allInfoRules(ipMaskScope) {
		if _, ok := selected[rule.name]; ok {
			detector.rules = append(detector.rules, rule)
		}
	}
	return detector, nil
}

// FindAll 返回按文本位置排序且不重叠的敏感信息命中。
func (d *InfoDetector) FindAll(text string) []InfoMatch {
	if d == nil || !d.enabled || len(d.rules) == 0 || text == "" {
		return nil
	}

	candidates := make([]infoCandidateWithRule, 0)
	for ruleIndex, rule := range d.rules {
		for _, candidate := range findRuleCandidates(rule, text) {
			candidates = append(candidates, infoCandidateWithRule{
				infoCandidate: candidate,
				ruleIndex:     ruleIndex,
				ruleName:      rule.name,
			})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].start != candidates[j].start {
			return candidates[i].start < candidates[j].start
		}
		if candidates[i].end != candidates[j].end {
			return candidates[i].end > candidates[j].end
		}
		return candidates[i].ruleIndex < candidates[j].ruleIndex
	})

	matches := make([]InfoMatch, 0, len(candidates))
	lastEnd := -1
	for _, candidate := range candidates {
		if candidate.start < lastEnd {
			continue
		}
		matches = append(matches, InfoMatch{
			Rule:        candidate.ruleName,
			Original:    text[candidate.start:candidate.end],
			Replacement: candidate.replacement,
			Start:       candidate.start,
			End:         candidate.end,
		})
		lastEnd = candidate.end
	}
	return matches
}

// Mask 掩码替换请求文本，并返回命中明细。
func (d *InfoDetector) Mask(text string) (string, []InfoMatch) {
	matches := d.FindAll(text)
	if len(matches) == 0 {
		return text, nil
	}

	var builder strings.Builder
	builder.Grow(len(text))
	last := 0
	for _, match := range matches {
		builder.WriteString(text[last:match.Start])
		builder.WriteString(match.Replacement)
		last = match.End
	}
	builder.WriteString(text[last:])
	return builder.String(), matches
}

type infoCandidateWithRule struct {
	infoCandidate
	ruleIndex int
	ruleName  string
}

func findRuleCandidates(rule infoRule, text string) []infoCandidate {
	if rule.find != nil {
		return rule.find(text)
	}
	indices := rule.regex.FindAllStringIndex(text, -1)
	candidates := make([]infoCandidate, 0, len(indices))
	for _, index := range indices {
		original := text[index[0]:index[1]]
		candidates = append(candidates, infoCandidate{
			start:       index[0],
			end:         index[1],
			replacement: rule.mask(original),
		})
	}
	return candidates
}

func validateInfoRules(rules []string) error {
	allowed := map[string]struct{}{
		config.SensitiveInfoRulePhone:     {},
		config.SensitiveInfoRuleIDCard:    {},
		config.SensitiveInfoRuleEmail:     {},
		config.SensitiveInfoRuleIPAddress: {},
		config.SensitiveInfoRuleBankCard:  {},
	}
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		if strings.TrimSpace(rule) == "" {
			return fmt.Errorf("敏感信息规则名不能为空")
		}
		if _, ok := allowed[rule]; !ok {
			return fmt.Errorf("不支持的敏感信息规则 %q", rule)
		}
		if _, ok := seen[rule]; ok {
			return fmt.Errorf("敏感信息规则 %q 重复", rule)
		}
		seen[rule] = struct{}{}
	}
	return nil
}

// normalizeIPMaskScope 归一化 IP 掩码范围：合法值原样返回，零值继续按 public
// 处理以兼容未填该字段的既有直接调用方，未知取值显式报错。
func normalizeIPMaskScope(scope string) (string, error) {
	switch scope {
	case config.SensitiveInfoIPMaskScopePublic, config.SensitiveInfoIPMaskScopeAll:
		return scope, nil
	case "":
		return config.SensitiveInfoIPMaskScopePublic, nil
	default:
		return "", fmt.Errorf("不支持的 IP 掩码范围 %q", scope)
	}
}

func allInfoRules(ipMaskScope string) []infoRule {
	return []infoRule{
		{
			name:  config.SensitiveInfoRulePhone,
			regex: regexp.MustCompile(`\b1[3-9][0-9]{9}\b`),
			mask:  maskPhone,
		},
		{
			name: config.SensitiveInfoRuleIDCard,
			find: findIDCardCandidates,
		},
		{
			name:  config.SensitiveInfoRuleEmail,
			regex: regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`),
			mask:  maskEmail,
		},
		{
			name: config.SensitiveInfoRuleIPAddress,
			find: func(text string) []infoCandidate {
				return findIPCandidates(text, ipMaskScope)
			},
		},
		{
			name: config.SensitiveInfoRuleBankCard,
			find: findBankCardCandidates,
		},
	}
}

func findBankCardCandidates(text string) []infoCandidate {
	indices := bankCardPattern.FindAllStringSubmatchIndex(text, -1)
	candidates := make([]infoCandidate, 0, len(indices))
	for _, index := range indices {
		if len(index) < 4 || index[2] < 0 {
			continue
		}
		original := text[index[2]:index[3]]
		digits := strings.NewReplacer(" ", "", "-", "").Replace(original)
		if !validLuhn(digits) {
			continue
		}
		candidates = append(candidates, infoCandidate{
			start: index[2], end: index[3], replacement: "[MASKED_PII:bank_card]",
		})
	}
	return candidates
}

func validLuhn(value string) bool {
	if len(value) < 13 || len(value) > 19 {
		return false
	}
	sum := 0
	double := false
	for index := len(value) - 1; index >= 0; index-- {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
		digit := int(value[index] - '0')
		if double {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
		double = !double
	}
	return sum%10 == 0
}

func findIDCardCandidates(text string) []infoCandidate {
	indices := idCardPattern.FindAllStringIndex(text, -1)
	candidates := make([]infoCandidate, 0, len(indices))
	for _, index := range indices {
		original := text[index[0]:index[1]]
		if !validChineseIDCard(original) {
			continue
		}
		candidates = append(candidates, infoCandidate{
			start:       index[0],
			end:         index[1],
			replacement: "[MASKED_PII:id_card]",
		})
	}
	return candidates
}

func validChineseIDCard(value string) bool {
	if len(value) != 18 {
		return false
	}
	for index := 0; index < 17; index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	birthDate, err := time.Parse("20060102", value[6:14])
	if err != nil || birthDate.Year() < 1800 || birthDate.After(time.Now().UTC()) {
		return false
	}
	weights := [...]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	checks := "10X98765432"
	sum := 0
	for index, weight := range weights {
		sum += int(value[index]-'0') * weight
	}
	expected := checks[sum%11]
	actual := value[17]
	if actual == 'x' {
		actual = 'X'
	}
	return actual == expected
}

// findIPCandidates 查找文本中的 IP 地址候选。public 范围下跳过私网、保留、
// 文档与组播等无个人定位价值的地址；版本号语境中的四段数字视为版本号放行。
func findIPCandidates(text string, ipMaskScope string) []infoCandidate {
	maskAll := ipMaskScope == config.SensitiveInfoIPMaskScopeAll
	candidates := make([]infoCandidate, 0)
	for _, index := range ipv4Pattern.FindAllStringIndex(text, -1) {
		original := text[index[0]:index[1]]
		ip := net.ParseIP(original)
		if ip == nil {
			continue
		}
		if !maskAll && isNonPublicIP(ip) {
			continue
		}
		if hasVersionNumberHint(text, index[0]) {
			continue
		}
		candidates = append(candidates, infoCandidate{start: index[0], end: index[1], replacement: "[MASKED_PII:ip_address]"})
	}

	for _, index := range ipv6Pattern.FindAllStringIndex(text, -1) {
		original := text[index[0]:index[1]]
		ip := net.ParseIP(original)
		if ip == nil {
			continue
		}
		if !maskAll && isNonPublicIP(ip) {
			continue
		}
		candidates = append(candidates, infoCandidate{start: index[0], end: index[1], replacement: "[MASKED_PII:ip_address]"})
	}
	return candidates
}

// isNonPublicIP 判断地址是否属于私网、回环、链路本地、组播、未指定地址，
// 或 extraNonPublicIPNets 列出的保留网段。这些地址无法定位到具体个人。
func isNonPublicIP(ip net.IP) bool {
	return ip.IsPrivate() ||
		ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified() ||
		containsIPNet(extraNonPublicIPNets, ip)
}

func containsIPNet(nets []*net.IPNet, ip net.IP) bool {
	for _, ipNet := range nets {
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

// hasVersionNumberHint 判断 IPv4 命中位置之前是否紧跟版本号语境词。
// 只认高置信度关键词，不做语义猜测：宁可少放行，也不漏掉真实公网 IP 的掩码。
func hasVersionNumberHint(text string, matchStart int) bool {
	windowStart := matchStart - versionHintWindow
	if windowStart < 0 {
		windowStart = 0
	}
	return versionHintPattern.MatchString(text[windowStart:matchStart])
}

func maskPhone(value string) string {
	return "[MASKED_PII:phone]"
}

func maskEmail(value string) string {
	return "[MASKED_PII:email]"
}
