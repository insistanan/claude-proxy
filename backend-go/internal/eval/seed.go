package eval

import (
	"fmt"
	"log"
)

// seedProbes 是内置题库的唯一出处。这些题带 builtin 标记，每次启动按这里的定义覆盖 DB。
func seedProbes() []Probe {
	probes := []Probe{
		{
			Slug:        "auth-model-echo",
			Name:        "模型回显",
			Description: "让模型自报身份；中转若改了默认 model，这一题会与请求模型对不上（pass=如实回显）。",
			Category:    CategoryAuthenticity,
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
			Slug:        "auth-protocol-shape",
			Name:        "协议外形",
			Description: "看响应是否带 usage、content type=text；中转若丢字段或改成其他形态，这一题会判 fail。",
			Category:    CategoryAuthenticity,
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
			Slug:        "auth-signature",
			Name:        "思考签名",
			Description: "仅 Claude 适用：观察思考块是否带 thought_signature；伪造/缺失的中转在这里露馅。",
			Category:    CategoryAuthenticity,
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
			Slug:        "auth-juice",
			Name:        "思考用量",
			Description: "观测是否真的产生了思考 token；假思考/零思考的中转会被记 suspect。",
			Category:    CategoryAuthenticity,
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
			Slug:        "auth-random-numbers",
			Name:        "随机数指纹",
			Description: "同题采样 25 次取随机数做分布比对：全相同判 fail，跨渠道直方图高度相似只标注「指纹相近」供人工复核。",
			Category:    CategoryAuthenticity,
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
			Slug:        "iq-candy-21",
			Name:        "糖果抽屉",
			Description: "抽屉原理数学题，标准答案 21；答案错说明模型或映射有问题。",
			Category:    CategoryIQ,
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
			Slug:        "iq-china-capital",
			Name:        "闭卷常识",
			Description: "最简单的常识题，答「北京」；连这都答错基本可断定映射到了残血模型。",
			Category:    CategoryIQ,
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
			Slug:        "iq-svg-circle",
			Name:        "SVG 圆",
			Description: "要求输出一段合法 SVG；判定会真的解析几何（半径/圆心/颜色），结果抽屉里可预览。",
			Category:    CategoryIQ,
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
			Slug:        "iq-svg-rects",
			Name:        "SVG 双矩形",
			Description: "两个不重叠矩形的 SVG；考察模型能否保持几何约束不穿帮。",
			Category:    CategoryIQ,
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
			Slug:        "iq-rubric-tradeoff",
			Name:        "开放权衡",
			Description: "开放论述题，由被测渠道自己当裁判再评一次（额外 1 次请求）；考察表达与归纳质量。",
			Category:    CategoryIQ,
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
			Slug:        "authenticity-cheap",
			Name:        "真伪快检",
			Description: "4 道便宜真伪题（模型回显 / 协议外形 / 思考签名 / 思考用量），每渠道 ≤ 4 次请求，适合挂值班定时复查。",
			ProbeIDs: []string{
				ids["auth-model-echo"], ids["auth-protocol-shape"], ids["auth-signature"], ids["auth-juice"],
			},
			Cheap: true,
		},
		{
			Slug:        "authenticity-full",
			Name:        "真伪完整",
			Description: "在快检基础上加随机数指纹（25 次采样），用于揪同源假渠道；不便宜，不能挂值班。",
			ProbeIDs: []string{
				ids["auth-model-echo"], ids["auth-protocol-shape"], ids["auth-signature"], ids["auth-juice"], ids["auth-random-numbers"],
			},
			Cheap: false,
		},
		{
			Slug:        "iq-light",
			Name:        "轻量智商",
			Description: "5 道轻量智商题（数学、常识、SVG、开放权衡），直观判断回答质量；含 rubric 自评（额外 1 次请求），整体不算便宜，不能挂值班。",
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
