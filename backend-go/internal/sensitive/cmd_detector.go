package sensitive

import (
	"fmt"
	"sort"
	"strings"

	"github.com/BenedictKing/api-proxy/internal/config"
)

const maxCommandWindow = 4096

// CommandMatch 描述一次可执行上下文中的危险命令命中。
type CommandMatch struct {
	Rule    string `json:"rule"`
	Command string `json:"command"`
	Context string `json:"context"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
}

// CmdDetector 只对代码块或工具参数执行危险命令检测。
type CmdDetector struct {
	enabled bool
	rules   []commandRule
}

// NewCmdDetector 根据设置创建危险命令检测器。
func NewCmdDetector(settings config.DangerousCmdConfig) (*CmdDetector, error) {
	if err := validateCommandRules(settings.EnabledRules); err != nil {
		return nil, err
	}
	detector := &CmdDetector{enabled: settings.Enabled}
	if !settings.Enabled {
		return detector, nil
	}
	selected := make(map[string]struct{}, len(settings.EnabledRules))
	for _, rule := range settings.EnabledRules {
		selected[rule] = struct{}{}
	}
	for _, rule := range builtinCommandRules {
		if _, ok := selected[rule.name]; ok {
			detector.rules = append(detector.rules, rule)
		}
	}
	return detector, nil
}

// Find 检测 Markdown 代码围栏内的危险命令，普通文本不会触发匹配。
func (d *CmdDetector) Find(text string) []CommandMatch {
	if d == nil || !d.enabled || len(d.rules) == 0 || text == "" {
		return nil
	}
	matches := make([]CommandMatch, 0)
	for _, span := range codeBlockSpans(text) {
		matches = append(matches, d.findInSegment(text[span.start:span.end], "code_block", span.start)...)
	}
	return matches
}

// FindInToolArguments 递归检测工具参数中的字符串值。
func (d *CmdDetector) FindInToolArguments(value any) []CommandMatch {
	if d == nil || !d.enabled || len(d.rules) == 0 {
		return nil
	}
	matches := make([]CommandMatch, 0)
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case string:
			matches = append(matches, d.findInSegment(typed, "tool", 0)...)
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				walk(typed[key])
			}
		}
	}
	walk(value)
	return matches
}

// Contains 返回文本代码块或工具参数中是否命中危险命令。
func (d *CmdDetector) Contains(text string) bool {
	return len(d.Find(text)) > 0
}

func (d *CmdDetector) findInSegment(segment, context string, base int) []CommandMatch {
	matches := make([]CommandMatch, 0)
	for _, rule := range d.rules {
		for _, index := range rule.regex.FindAllStringIndex(segment, -1) {
			matches = append(matches, CommandMatch{
				Rule:    rule.name,
				Command: segment[index[0]:index[1]],
				Context: context,
				Start:   base + index[0],
				End:     base + index[1],
			})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Start != matches[j].Start {
			return matches[i].Start < matches[j].Start
		}
		return matches[i].End < matches[j].End
	})
	return matches
}

type codeSpan struct {
	start int
	end   int
}

func codeBlockSpans(text string) []codeSpan {
	spans := make([]codeSpan, 0)
	searchFrom := 0
	for searchFrom < len(text) {
		opening := strings.Index(text[searchFrom:], "```")
		if opening < 0 {
			break
		}
		opening += searchFrom
		contentStart := opening + len("```")
		closing := strings.Index(text[contentStart:], "```")
		if closing < 0 {
			spans = append(spans, codeSpan{start: contentStart, end: len(text)})
			break
		}
		closing += contentStart
		spans = append(spans, codeSpan{start: contentStart, end: closing})
		searchFrom = closing + len("```")
	}
	return spans
}

func validateCommandRules(rules []string) error {
	allowed := make(map[string]struct{}, len(builtinCommandRules))
	for _, rule := range builtinCommandRules {
		allowed[rule.name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		if strings.TrimSpace(rule) == "" {
			return fmt.Errorf("危险命令规则名不能为空")
		}
		if _, ok := allowed[rule]; !ok {
			return fmt.Errorf("不支持的危险命令规则 %q", rule)
		}
		if _, ok := seen[rule]; ok {
			return fmt.Errorf("危险命令规则 %q 重复", rule)
		}
		seen[rule] = struct{}{}
	}
	return nil
}

// StreamCmdScanner 在流式响应中保留最近 4KB 的代码内容并检测跨 chunk 命令。
type StreamCmdScanner struct {
	detector *CmdDetector
	window   string
	pending  string
	inCode   bool
	blocked  *CommandMatch
}

// NewStreamCmdScanner 创建流式危险命令扫描器。
func NewStreamCmdScanner(detector *CmdDetector) *StreamCmdScanner {
	return &StreamCmdScanner{detector: detector}
}

// Feed 输入一个响应片段；命中后后续调用仍返回同一命中。
func (s *StreamCmdScanner) Feed(chunk string) (*CommandMatch, bool) {
	if s == nil || s.detector == nil || !s.detector.enabled {
		return nil, false
	}
	if s.blocked != nil {
		return s.blocked, true
	}
	input := s.pending + chunk
	s.pending = ""
	body, suffix := splitFenceSuffix(input)
	s.pending = suffix
	for position := 0; position < len(body); {
		fence := strings.Index(body[position:], "```")
		if fence < 0 {
			if s.inCode {
				if match := s.appendCode(body[position:], false); match != nil {
					s.blocked = match
					return match, true
				}
			}
			break
		}
		fence += position
		if s.inCode {
			if match := s.appendCode(body[position:fence], true); match != nil {
				s.blocked = match
				return match, true
			}
		}
		s.inCode = !s.inCode
		if !s.inCode {
			s.window = ""
		}
		position = fence + len("```")
	}
	return nil, false
}

// Flush 处理流末尾未闭合的围栏前缀。
func (s *StreamCmdScanner) Flush() (*CommandMatch, bool) {
	if s == nil {
		return nil, false
	}
	if s.blocked != nil {
		return s.blocked, s.blocked != nil
	}
	if s.inCode {
		if match := s.appendCode(s.pending, true); match != nil {
			s.blocked = match
			return match, true
		}
	}
	s.pending = ""
	return nil, false
}

func (s *StreamCmdScanner) appendCode(fragment string, final bool) *CommandMatch {
	s.window += fragment
	if len(s.window) > maxCommandWindow {
		s.window = s.window[len(s.window)-maxCommandWindow:]
	}
	for _, match := range s.detector.findInSegment(s.window, "code_block", 0) {
		if final || match.End < len(s.window) || endsWithCommandBoundary(s.window[:match.End]) {
			selected := match
			return &selected
		}
	}
	return nil
}

func splitFenceSuffix(input string) (string, string) {
	if strings.HasSuffix(input, "```") {
		return input, ""
	}
	backticks := 0
	for backticks < 2 && backticks < len(input) && input[len(input)-1-backticks] == '`' {
		backticks++
	}
	if backticks == 0 {
		return input, ""
	}
	return input[:len(input)-backticks], input[len(input)-backticks:]
}

func endsWithCommandBoundary(value string) bool {
	if value == "" {
		return false
	}
	switch value[len(value)-1] {
	case '\n', '\r', ';', '&', '|':
		return true
	default:
		return false
	}
}
