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
	idCardPattern = regexp.MustCompile(`\b[1-9][0-9]{16}[0-9Xx]\b`)
	ipv4Pattern   = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)
	ipv6Pattern   = regexp.MustCompile(`(?i)[0-9a-f:][0-9a-f:.]*:[0-9a-f:.]*`)
)

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
	detector := &InfoDetector{enabled: settings.Enabled}
	if !settings.Enabled {
		return detector, nil
	}

	selected := make(map[string]struct{}, len(settings.EnabledRules))
	for _, rule := range settings.EnabledRules {
		selected[rule] = struct{}{}
	}
	for _, rule := range allInfoRules() {
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

func allInfoRules() []infoRule {
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
			find: findIPCandidates,
		},
	}
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

func findIPCandidates(text string) []infoCandidate {
	candidates := make([]infoCandidate, 0)
	for _, index := range ipv4Pattern.FindAllStringIndex(text, -1) {
		original := text[index[0]:index[1]]
		if net.ParseIP(original) == nil {
			continue
		}
		candidates = append(candidates, infoCandidate{start: index[0], end: index[1], replacement: "[MASKED_PII:ip_address]"})
	}

	for _, index := range ipv6Pattern.FindAllStringIndex(text, -1) {
		original := text[index[0]:index[1]]
		if net.ParseIP(original) == nil {
			continue
		}
		candidates = append(candidates, infoCandidate{start: index[0], end: index[1], replacement: "[MASKED_PII:ip_address]"})
	}
	return candidates
}

func maskPhone(value string) string {
	return "[MASKED_PII:phone]"
}

func maskIDCard(value string) string {
	return "[MASKED_PII:id_card]"
}

func maskEmail(value string) string {
	return "[MASKED_PII:email]"
}
