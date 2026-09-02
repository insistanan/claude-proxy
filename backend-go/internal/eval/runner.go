package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
)

var ErrBusy = errors.New("已有评测在跑")

type Runner struct {
	store     *Store
	sender    *Sender
	cfg       *config.ConfigManager
	scheduler *scheduler.ChannelScheduler

	mu        sync.Mutex
	busy      bool
	currentID string
	cancel    context.CancelFunc
}

func NewRunner(store *Store, sender *Sender, cfg *config.ConfigManager, sch *scheduler.ChannelScheduler) *Runner {
	return &Runner{store: store, sender: sender, cfg: cfg, scheduler: sch}
}

func (r *Runner) Busy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.busy
}

func (r *Runner) CurrentRunID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.currentID
}

func (r *Runner) Cancel() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.busy || r.cancel == nil {
		return fmt.Errorf("当前没有评测在跑")
	}
	r.cancel()
	return nil
}

func EstimateCalls(probes []Probe, channelCount int) int {
	total := 0
	for _, probe := range probes {
		samples := probe.SampleCount
		if samples < 1 {
			samples = 1
		}
		calls := samples
		if probe.Judge.Kind == JudgeRubric {
			calls += samples
		}
		total += calls * channelCount
	}
	return total
}

func (r *Runner) Start(req StartRunRequest) (Run, error) {
	if len(req.ChannelIDs) == 0 {
		return Run{}, fmt.Errorf("至少勾选一个渠道")
	}

	var suite Suite
	var probes []Probe
	var err error

	if req.SuiteID == "custom" || strings.TrimSpace(req.SuiteID) == "" {
		suite = Suite{
			ID:          "custom",
			Slug:        "custom",
			Name:        "自选题目",
			Description: "自选题库评测",
		}
		if len(req.ProbeIDs) > 0 {
			probes, err = r.store.ProbesByIDs(req.ProbeIDs)
			if err != nil {
				return Run{}, err
			}
		} else {
			probes, err = r.store.ListProbes()
			if err != nil {
				return Run{}, err
			}
		}
	} else {
		suite, probes, err = r.store.SuiteWithProbes(req.SuiteID)
		if err != nil {
			return Run{}, err
		}
		// 如果前端明确传入了 ProbeIDs，优先根据 ProbeIDs 完整加载（支持包含套件外自建题）
		if len(req.ProbeIDs) > 0 {
			wantedProbes, fetchErr := r.store.ProbesByIDs(req.ProbeIDs)
			if fetchErr == nil && len(wantedProbes) > 0 {
				probes = wantedProbes
			} else {
				wanted := make(map[string]struct{}, len(req.ProbeIDs))
				for _, id := range req.ProbeIDs {
					wanted[id] = struct{}{}
				}
				filtered := make([]Probe, 0, len(probes))
				for _, probe := range probes {
					if _, ok := wanted[probe.ID]; ok {
						filtered = append(filtered, probe)
					}
				}
				probes = filtered
			}
		}
	}

	if len(probes) == 0 {
		return Run{}, fmt.Errorf("评测题目列表为空，请先选择要评测的题目")
	}
	if req.Trigger == "" {
		req.Trigger = TriggerManual
	}

	r.mu.Lock()
	if r.busy {
		r.mu.Unlock()
		return Run{}, ErrBusy
	}
	run, err := r.store.CreateRun(Run{
		SuiteID:        suite.ID,
		SuiteName:      suite.Name,
		Trigger:        req.Trigger,
		Status:         RunQueued,
		ChannelIDs:     req.ChannelIDs,
		Model:          strings.TrimSpace(req.Model),
		ChannelModels:  normalizeChannelModels(req.ChannelModels, req.ChannelIDs),
		Thinking:       firstNonEmpty(req.Thinking, ThinkingInherit),
		EstimatedCalls: EstimateCalls(probes, len(req.ChannelIDs)),
	})
	if err != nil {
		r.mu.Unlock()
		return Run{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.busy = true
	r.currentID = run.ID
	r.cancel = cancel
	r.mu.Unlock()

	go r.execute(ctx, run, suite, probes)
	return run, nil
}

func (r *Runner) execute(ctx context.Context, run Run, suite Suite, probes []Probe) {
	defer func() {
		r.mu.Lock()
		r.busy = false
		r.currentID = ""
		r.cancel = nil
		r.mu.Unlock()
	}()

	run.Status = RunRunning
	run.StartedAt = time.Now().Unix()
	if err := r.store.UpdateRun(run); err != nil {
		// 写不进去也不能让批次烂在 queued：前端会一直等一个永远不来的终态。
		log.Printf("[Eval-Run] 标记批次开始失败: %v", err)
		if _, finishErr := r.finish(run, RunFailed, fmt.Sprintf("标记批次开始失败: %v", err)); finishErr != nil {
			log.Printf("[Eval-Run] 保存失败终态失败: %v", finishErr)
		}
		return
	}
	log.Printf("[Eval-Run] 开始 %s 套件=%s 渠道=%d 预估请求=%d trigger=%s", run.ID, suite.Name, len(run.ChannelIDs), run.EstimatedCalls, run.Trigger)

	var stored []Result
	var persistenceErrors []error
	cancelled := false
	for _, channelID := range run.ChannelIDs {
		if ctx.Err() != nil {
			cancelled = true
			break
		}
		for _, probe := range probes {
			if ctx.Err() != nil {
				cancelled = true
				break
			}
			result := r.runProbe(ctx, run, channelID, probe)
			saved, err := r.store.InsertResult(result)
			if err != nil {
				persistenceErrors = append(persistenceErrors, fmt.Errorf("写入结果 channel=%s probe=%s: %w", channelID, probe.ID, err))
				continue
			}
			stored = append(stored, saved)
		}
	}

	if !cancelled {
		if err := r.compareFingerprints(stored); err != nil {
			persistenceErrors = append(persistenceErrors, err)
		}
	}

	status := RunDone
	if cancelled {
		status = RunCancelled
	}
	run.FinishedAt = time.Now().Unix()
	if err := r.writeLatest(run, suite, stored); err != nil {
		persistenceErrors = append(persistenceErrors, err)
	}
	errMessage := ""
	if len(persistenceErrors) > 0 {
		errMessage = errors.Join(persistenceErrors...).Error()
		if status == RunDone {
			status = RunPartial
		}
	}
	var finishErr error
	run, finishErr = r.finish(run, status, errMessage)
	if finishErr != nil {
		log.Printf("[Eval-Run] 保存批次终态失败: %v", finishErr)
	}
	log.Printf("[Eval-Run] 结束 %s status=%s", run.ID, run.Status)
}

// finish 给批次盖终态。状态和错误一起落盘，保证前端总能等到一个终态而不是无限转圈。
func (r *Runner) finish(run Run, status, errMessage string) (Run, error) {
	run.Status = status
	run.Error = errMessage
	if run.FinishedAt == 0 {
		run.FinishedAt = time.Now().Unix()
	}
	return run, r.store.UpdateRun(run)
}

func (r *Runner) runProbe(ctx context.Context, run Run, channelID string, probe Probe) Result {
	result := Result{
		RunID:     run.ID,
		ChannelID: channelID,
		ProbeID:   probe.ID,
		ProbeName: probe.Name,
		Detail:    map[string]interface{}{},
	}
	located, ok := r.cfg.FindChannelByID(channelID)
	if !ok {
		result.Verdict = VerdictInapplicable
		result.Detail["reason"] = "渠道已不存在"
		return result
	}
	if reason := inapplicableReason(located, probe, r.scheduler); reason != "" {
		result.Verdict = VerdictInapplicable
		result.Detail["reason"] = reason
		return result
	}

	options := SendOptions{ModelOverride: run.ModelForChannel(channelID), ThinkingOverride: run.Thinking}
	var samples []Extracted
	var lastExcerpt string
	var totalLatency int64
	sampleCount := probe.SampleCount
	if sampleCount < 1 {
		sampleCount = 1
	}
	for i := 0; i < sampleCount; i++ {
		if ctx.Err() != nil {
			result.Verdict = VerdictError
			result.Detail["reason"] = "评测已取消"
			return result
		}
		raw, err := r.sender.Send(ctx, located, probe.Stimulus, options)
		if raw != nil {
			totalLatency += raw.LatencyMS
		}
		if err != nil {
			result.Verdict = VerdictError
			result.Excerpt = lastExcerpt
			result.LatencyMS = totalLatency
			result.Detail["reason"] = err.Error()
			return result
		}
		extracted, extractErr := ExtractFromResponse(probe.Extract.Kind, raw)
		if extractErr != nil {
			result.Verdict = VerdictInsufficient
			result.Excerpt = extracted.Text
			result.LatencyMS = totalLatency
			result.Detail["reason"] = extractErr.Error()
			return result
		}
		lastExcerpt = extracted.Text
		samples = append(samples, extracted)
	}
	result.LatencyMS = totalLatency

	if probe.Judge.Kind == JudgeRubric {
		verdict, excerpt, detail := r.judgeRubric(ctx, located, probe, samples[0], options)
		result.Verdict = verdict
		result.Excerpt = excerpt
		result.Detail = detail
		return result
	}

	requestedModel := rawRequestedModel(located, run.ModelForChannel(channelID))
	verdict, excerpt, detail := Judge(JudgeInput{Probe: probe, RequestedModel: requestedModel, Samples: samples})
	result.Verdict = verdict
	result.Excerpt = excerpt
	result.Detail = detail
	return result
}

func rawRequestedModel(located config.LocatedChannel, override string) string {
	model, err := resolveEvalModel(located.Upstream, override)
	if err != nil {
		return override
	}
	return model
}

func (r *Runner) judgeRubric(ctx context.Context, located config.LocatedChannel, probe Probe, sample Extracted, options SendOptions) (string, string, map[string]interface{}) {
	envelope := Stimulus{
		Prompt: fmt.Sprintf(`你是评测裁判。根据下列分析说明，判断候选回答是否达标。
只输出一个 JSON 对象，不要 markdown，不要其它文字。格式：
{"verdict":"pass|suspect|fail","reasons":["..."],"evidence":"..."}

分析说明：
%s

候选回答：
%s`, probe.Judge.AnalysisPrompt, sample.Text),
		MaxTokens:   256,
		Temperature: 0,
		ForceJSON:   true,
	}
	raw, err := r.sender.Send(ctx, located, envelope, options)
	if err != nil {
		return VerdictError, sample.Text, map[string]interface{}{"reason": "信封自评请求失败: " + err.Error()}
	}
	extracted, extractErr := ExtractFromResponse(ExtractText, raw)
	if extractErr != nil {
		return VerdictError, sample.Text, map[string]interface{}{"reason": "信封自评无法抽出文本: " + extractErr.Error()}
	}
	payload := extractJSONObject(extracted.Text)
	if payload == nil {
		return VerdictError, sample.Text, map[string]interface{}{"reason": "信封自评不是合法 JSON", "judgeText": extracted.Text}
	}
	verdict := strings.ToLower(strings.TrimSpace(asString(payload["verdict"])))
	switch verdict {
	case VerdictPass, VerdictSuspect, VerdictFail:
	default:
		return VerdictError, sample.Text, map[string]interface{}{"reason": "信封自评 verdict 非法", "judgeText": extracted.Text}
	}
	reasons := parseRubricReasons(payload)
	evidence := strings.TrimSpace(asString(payload["evidence"]))
	return verdict, sample.Text, map[string]interface{}{
		"reason":    strings.Join(reasons, "；"),
		"reasons":   reasons,
		"evidence":  evidence,
		"judgeText": extracted.Text,
		"candidate": sample.Text,
	}
}

func extractJSONObject(text string) map[string]interface{} {
	text = strings.TrimSpace(text)
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(text[start:end+1]), &payload); err != nil {
		return nil
	}
	return payload
}

func parseRubricReasons(payload map[string]interface{}) []string {
	if payload == nil {
		return nil
	}
	if raw, ok := payload["reasons"]; ok {
		switch typed := raw.(type) {
		case []interface{}:
			var reasons []string
			for _, item := range typed {
				text := strings.TrimSpace(asString(item))
				if text != "" {
					reasons = append(reasons, text)
				}
			}
			if len(reasons) > 0 {
				return reasons
			}
		case string:
			if text := strings.TrimSpace(typed); text != "" {
				return []string{text}
			}
		}
	}
	if reason := strings.TrimSpace(asString(payload["reason"])); reason != "" {
		return []string{reason}
	}
	return nil
}

func inapplicableReason(located config.LocatedChannel, probe Probe, sch *scheduler.ChannelScheduler) string {
	if located.Kind == KindImages {
		return "Images 协议不参与评测"
	}
	status := config.GetChannelStatus(&located.Upstream)
	switch status {
	case config.ChannelStatusDisabled:
		return "渠道在备用池"
	case config.ChannelStatusDeprecated, config.ChannelStatusDeleted:
		return "渠道已弃用"
	case config.ChannelStatusSuspended:
		return "渠道已熔断"
	}
	if sch != nil && len(located.Upstream.APIKeys) > 0 {
		baseURLs := located.Upstream.GetAllBaseURLs()
		if len(baseURLs) > 0 && sch.ShouldSuspendKey(baseURLs[0], located.Upstream.APIKeys[0], located.Index, scheduler.ChannelKind(located.Kind)) {
			return "渠道已熔断"
		}
	}
	if len(probe.ApplicableServiceTypes) > 0 && !containsString(probe.ApplicableServiceTypes, located.Upstream.ServiceType) {
		return "探针不适用于 serviceType=" + located.Upstream.ServiceType
	}
	return ""
}

// compareFingerprints 批次跑完后做跨渠道随机数指纹比对。只回填 detail 供抽屉展示，不改 verdict——
// 指纹相近只是"值得人看一眼"，判成 fail 会误伤同源但合法的官方中转。
func (r *Runner) compareFingerprints(results []Result) error {
	var persistenceErrors []error
	byProbe := map[string][]Result{}
	for _, result := range results {
		if result.Verdict == VerdictInapplicable || result.Verdict == VerdictError {
			continue
		}
		byProbe[result.ProbeID] = append(byProbe[result.ProbeID], result)
	}
	for probeID, group := range byProbe {
		if len(group) < 2 {
			continue
		}
		probe, err := r.store.GetProbe(probeID)
		if err != nil {
			persistenceErrors = append(persistenceErrors, fmt.Errorf("指纹比对读取探针 %s: %w", probeID, err))
			continue
		}
		if probe.Judge.Kind != JudgeDistribution || !probe.Judge.CompareHistogram {
			continue
		}

		var series []fingerprintSeries
		for _, item := range group {
			numbers := asFloatSlice(item.Detail["numbers"])
			if len(numbers) == 0 {
				continue
			}
			series = append(series, fingerprintSeries{result: item, numbers: numbers})
		}

		twins := map[string][]map[string]interface{}{}
		for i := 0; i < len(series); i++ {
			for j := i + 1; j < len(series); j++ {
				similarity := HistogramSimilarity(series[i].numbers, series[j].numbers)
				if similarity < FingerprintTwinThreshold {
					continue
				}
				left, right := series[i].result, series[j].result
				twins[left.ID] = append(twins[left.ID], fingerprintTwin(right.ChannelID, similarity))
				twins[right.ID] = append(twins[right.ID], fingerprintTwin(left.ChannelID, similarity))
				log.Printf("[Eval-Fingerprint] 渠道 %s 与 %s 随机数指纹相近 similarity=%.3f", left.ChannelID, right.ChannelID, similarity)
			}
		}

		for _, item := range series {
			list, ok := twins[item.result.ID]
			if !ok {
				continue
			}
			detail := item.result.Detail
			if detail == nil {
				detail = map[string]interface{}{}
			}
			detail["fingerprintTwins"] = list
			if err := r.store.UpdateResultDetail(item.result.ID, detail); err != nil {
				persistenceErrors = append(persistenceErrors, fmt.Errorf("指纹比对回填结果 %s: %w", item.result.ID, err))
			}
		}
	}
	if len(persistenceErrors) > 0 {
		return errors.Join(persistenceErrors...)
	}
	return nil
}

type fingerprintSeries struct {
	result  Result
	numbers []float64
}

func fingerprintTwin(channelID string, similarity float64) map[string]interface{} {
	return map[string]interface{}{"channelId": channelID, "similarity": similarity}
}

func (r *Runner) writeLatest(run Run, suite Suite, results []Result) error {
	byChannel := map[string][]string{}
	for _, result := range results {
		byChannel[result.ChannelID] = append(byChannel[result.ChannelID], result.Verdict)
	}
	var persistenceErrors []error
	for channelID, verdicts := range byChannel {
		aggregate := AggregateVerdicts(verdicts)
		if err := r.store.SaveChannelLatest(ChannelLatest{
			ChannelID:  channelID,
			RunID:      run.ID,
			SuiteName:  suite.Name,
			Aggregate:  aggregate,
			FinishedAt: run.FinishedAt,
		}, suite.ID); err != nil {
			persistenceErrors = append(persistenceErrors, fmt.Errorf("写入渠道最新结论 channel=%s: %w", channelID, err))
		}
	}
	if len(persistenceErrors) > 0 {
		return errors.Join(persistenceErrors...)
	}
	return nil
}

// AggregateVerdicts 把一个渠道本批次的多条结论收敛成一枚芯片。
// 优先级 fail > suspect > pass；error / insufficient / inapplicable 都是中性，不报警。
func AggregateVerdicts(verdicts []string) string {
	hasPass := false
	hasSuspect := false
	for _, verdict := range verdicts {
		switch verdict {
		case VerdictFail:
			return AggregateFail
		case VerdictSuspect:
			hasSuspect = true
		case VerdictPass:
			hasPass = true
		}
	}
	if hasSuspect {
		return AggregateSuspect
	}
	if hasPass {
		return AggregatePass
	}
	return AggregateNeutral
}

func AggregateLabel(aggregate string, finishedAt int64) (string, string) {
	if finishedAt > 0 && time.Since(time.Unix(finishedAt, 0)) > StaleAfter {
		return AggregateStale, "过期"
	}
	switch aggregate {
	case AggregatePass:
		return AggregatePass, "通过"
	case AggregateSuspect:
		return AggregateSuspect, "存疑"
	case AggregateFail:
		return AggregateFail, "失败"
	default:
		return AggregateNeutral, "观测"
	}
}
