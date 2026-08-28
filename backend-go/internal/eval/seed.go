package eval

import (
	"fmt"
	"log"
)

// seedProbes 是内置题库的唯一出处。这些题带 builtin 标记，每次启动按这里的定义覆盖 DB。
func seedProbes() []Probe {
	probes := []Probe{
		{
			Slug:     "auth-model-echo",
			Name:     "模型回显",
			Category: CategoryAuthenticity,
			Stimulus: Stimulus{
				Prompt:      "Reply with only the exact model identifier you are running as. No extra words.",
				MaxTokens:   64,
				Temperature: 0,
			},
			Extract:                ExtractSpec{Kind: ExtractProtocol},
			Judge:                  JudgeSpec{Kind: JudgeProtocol, ExpectModelEcho: true},
			SampleCount:            1,
			ApplicableServiceTypes: []string{ServiceClaude, ServiceOpenAI, ServiceGemini, ServiceResponses},
		},
		{
			Slug:     "auth-protocol-shape",
			Name:     "协议外形",
			Category: CategoryAuthenticity,
			Stimulus: Stimulus{
				Prompt:      "Reply with the single word pong.",
				MaxTokens:   32,
				Temperature: 0,
			},
			Extract:                ExtractSpec{Kind: ExtractProtocol},
			Judge:                  JudgeSpec{Kind: JudgeProtocol, ExpectUsage: true, ExpectContentTypes: []string{"text"}},
			SampleCount:            1,
			ApplicableServiceTypes: []string{ServiceClaude, ServiceOpenAI, ServiceGemini, ServiceResponses},
		},
		{
			Slug:     "auth-signature",
			Name:     "思考签名",
			Category: CategoryAuthenticity,
			Stimulus: Stimulus{
				Prompt:         "Think step by step about 17*19, then give the product only.",
				MaxTokens:      256,
				Temperature:    1,
				Thinking:       ThinkingEnabled,
				ThinkingBudget: 1024,
			},
			Extract:                ExtractSpec{Kind: ExtractProtocol},
			Judge:                  JudgeSpec{Kind: JudgeProtocol, ExpectSignature: "observe", ExpectThinking: "observe"},
			SampleCount:            1,
			ApplicableServiceTypes: []string{ServiceClaude},
		},
		{
			Slug:     "auth-juice",
			Name:     "思考用量",
			Category: CategoryAuthenticity,
			Stimulus: Stimulus{
				Prompt:         "Think carefully: is 7919 a prime number? Answer yes or no, then the smallest factor if no.",
				MaxTokens:      512,
				Temperature:    1,
				Thinking:       ThinkingEnabled,
				ThinkingBudget: 2048,
			},
			Extract:                ExtractSpec{Kind: ExtractUsage},
			Judge:                  JudgeSpec{Kind: JudgeObserve, ExpectThinkingUsage: true},
			SampleCount:            1,
			ApplicableServiceTypes: []string{ServiceClaude, ServiceOpenAI, ServiceGemini, ServiceResponses},
		},
		{
			Slug:     "auth-random-numbers",
			Name:     "随机数指纹",
			Category: CategoryAuthenticity,
			Stimulus: Stimulus{
				Prompt:      "Generate one uniformly random integer from 1 to 100 inclusive. Reply with the number only.",
				MaxTokens:   16,
				Temperature: 1,
			},
			Extract:                ExtractSpec{Kind: ExtractNumbers},
			Judge:                  JudgeSpec{Kind: JudgeDistribution, MinSamples: 20, FailIfIdentical: true, SuspectUniqueRatio: 0.2, CompareHistogram: true},
			SampleCount:            25,
			ApplicableServiceTypes: []string{ServiceClaude, ServiceOpenAI, ServiceGemini, ServiceResponses},
		},
		{
			Slug:     "iq-candy-21",
			Name:     "糖果抽屉",
			Category: CategoryIQ,
			Stimulus: Stimulus{
				Prompt:      "一个抽屉里有红、黄、蓝三种颜色的袜子各 10 双。至少要取出多少只袜子，才能保证一定有 3 双同色袜子？只回答一个整数。",
				MaxTokens:   32,
				Temperature: 0,
			},
			Extract:                ExtractSpec{Kind: ExtractText},
			Judge:                  JudgeSpec{Kind: JudgeNumeric, Expected: "21", ExpectedNumber: 21},
			SampleCount:            1,
			ApplicableServiceTypes: []string{ServiceClaude, ServiceOpenAI, ServiceGemini, ServiceResponses},
		},
		{
			Slug:     "iq-china-capital",
			Name:     "闭卷常识",
			Category: CategoryIQ,
			Stimulus: Stimulus{
				Prompt:      "中国的首都是哪座城市？只回答城市名。",
				MaxTokens:   16,
				Temperature: 0,
			},
			Extract:                ExtractSpec{Kind: ExtractText},
			Judge:                  JudgeSpec{Kind: JudgeExact, Expected: "北京", CaseInsensitive: true},
			SampleCount:            1,
			ApplicableServiceTypes: []string{ServiceClaude, ServiceOpenAI, ServiceGemini, ServiceResponses},
		},
		{
			Slug:     "iq-svg-circle",
			Name:     "SVG 圆",
			Category: CategoryIQ,
			Stimulus: Stimulus{
				Prompt:      "Output a valid SVG snippet (no markdown) that draws a red circle of radius 40 centered at (50,50) inside a 100x100 viewBox.",
				MaxTokens:   256,
				Temperature: 0,
			},
			Extract:                ExtractSpec{Kind: ExtractSVG},
			Judge:                  JudgeSpec{Kind: JudgeSVG},
			SampleCount:            1,
			ApplicableServiceTypes: []string{ServiceClaude, ServiceOpenAI, ServiceGemini, ServiceResponses},
		},
		{
			Slug:     "iq-svg-rects",
			Name:     "SVG 双矩形",
			Category: CategoryIQ,
			Stimulus: Stimulus{
				Prompt:      "Output a valid SVG snippet (no markdown) with a 200x100 viewBox containing two non-overlapping rectangles.",
				MaxTokens:   256,
				Temperature: 0,
			},
			Extract:                ExtractSpec{Kind: ExtractSVG},
			Judge:                  JudgeSpec{Kind: JudgeSVG},
			SampleCount:            1,
			ApplicableServiceTypes: []string{ServiceClaude, ServiceOpenAI, ServiceGemini, ServiceResponses},
		},
		{
			Slug:     "iq-rubric-tradeoff",
			Name:     "开放权衡",
			Category: CategoryIQ,
			Stimulus: Stimulus{
				Prompt:      "用不超过 120 字说明：在代理网关里，渠道评测为什么必须绕过调度器和协议转换器。给出至少两个会污染观测的具体机制。",
				MaxTokens:   256,
				Temperature: 0,
			},
			Extract: ExtractSpec{Kind: ExtractText},
			Judge: JudgeSpec{
				Kind: JudgeRubric,
				AnalysisPrompt: `判断回答是否同时满足：
1. 明确提到评测必须直连被测渠道，不能走生产调度。
2. 至少指出两个会污染观测的机制（例如：熔断/指标写入、对话记录、模型映射/协议转换改写 thinking 签名、视觉分流、渠道选择换成别的上游）。
3. 没有把“评测失败”等同于“渠道应被自动摘除”。
达标判 pass；只沾边或漏掉关键机制判 suspect；完全跑题或事实错误判 fail。`,
			},
			SampleCount:            1,
			ApplicableServiceTypes: []string{ServiceClaude, ServiceOpenAI, ServiceGemini, ServiceResponses},
		},
	}
	for index := range probes {
		probes[index].Builtin = true
	}
	return probes
}

// SyncBuiltins 把 seed.go 里的内置题库同步进 DB：缺的插入，已有的按代码覆盖。
// 覆盖是有意的——判定器和题目必须同版本，否则引擎升级后老题会静默变成另一种含义。
// 用户想定制就复制成自建题，那些不带 builtin 标记的一条都不动。
func (s *Store) SyncBuiltins() error {
	probes := seedProbes()
	for _, probe := range probes {
		if err := s.SyncBuiltinProbe(probe); err != nil {
			return fmt.Errorf("同步内置探针 %s: %w", probe.Slug, err)
		}
	}

	idOf := func(slug string) (string, error) {
		probe, err := s.GetProbeBySlug(slug)
		if err != nil {
			return "", fmt.Errorf("读取内置探针 %s: %w", slug, err)
		}
		return probe.ID, nil
	}
	ids := map[string]string{}
	for _, probe := range probes {
		id, err := idOf(probe.Slug)
		if err != nil {
			return err
		}
		ids[probe.Slug] = id
	}

	suites := []Suite{
		{
			Slug: "authenticity-cheap",
			Name: "真伪快检",
			ProbeIDs: []string{
				ids["auth-model-echo"], ids["auth-protocol-shape"], ids["auth-signature"], ids["auth-juice"],
			},
			Cheap: true,
		},
		{
			Slug: "authenticity-full",
			Name: "真伪完整",
			ProbeIDs: []string{
				ids["auth-model-echo"], ids["auth-protocol-shape"], ids["auth-signature"], ids["auth-juice"], ids["auth-random-numbers"],
			},
			Cheap: false,
		},
		{
			Slug: "iq-light",
			Name: "轻量智商",
			ProbeIDs: []string{
				ids["iq-candy-21"], ids["iq-china-capital"], ids["iq-svg-circle"], ids["iq-svg-rects"], ids["iq-rubric-tradeoff"],
			},
			Cheap: false,
		},
	}
	for _, suite := range suites {
		if err := s.SyncBuiltinSuite(suite); err != nil {
			return fmt.Errorf("同步内置套件 %s: %w", suite.Slug, err)
		}
	}

	watch, err := s.GetWatch()
	if err != nil {
		return err
	}
	if watch.SuiteID == "" {
		cheap, err := s.GetSuiteBySlug("authenticity-cheap")
		if err != nil {
			return err
		}
		watch.SuiteID = cheap.ID
		watch.Interval = Interval2h
		if err := s.PutWatch(watch); err != nil {
			return err
		}
	}
	log.Printf("[Eval-Seed] 内置题库已同步：%d 条探针 / %d 个套件", len(probes), len(suites))
	return nil
}
