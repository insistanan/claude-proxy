package modelaudit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const BuiltinCapabilityPackageID = "capability.pack.builtin-core"

func versionedRefKey(ref VersionedRef) string {
	return ref.ID + "\x00" + ref.SemanticVersion + "\x00" + ref.ImplementationVersion
}

// capabilityPresetModeOrder 返回预设 mode 的语义排序权重，
// 确保目录中预设按 quick→standard→deep 的自然顺序展示。
func capabilityPresetModeOrder(mode CapabilityPresetMode) int {
	switch mode {
	case CapabilityPresetQuick:
		return 0
	case CapabilityPresetStandard:
		return 1
	case CapabilityPresetDeep:
		return 2
	default:
		return 3
	}
}

type BuiltinCapabilityAssets struct {
	mu                sync.RWMutex
	packageSnapshot   CapabilityTaskPackageSnapshot
	packages          map[string]CapabilityTaskPackageSnapshot
	presets           map[string]CapabilityRunPreset
	evaluationOptions []CapabilityEvaluationOption
	tasks             map[string]CapabilityTaskDefinition
	taskPackages      map[string]VersionedRef
	questions         map[string]QuestionBankQuestion
	scorers           map[CapabilityScorerKind]CapabilityScorer
	questionBanks     QuestionBankCatalogSnapshot
	sourceRoot        string
	cacheRoot         string
}

func NewBuiltinCapabilityAssets() (*BuiltinCapabilityAssets, error) {
	scorers, err := builtinCapabilityScorers()
	if err != nil {
		return nil, err
	}
	exact := scorers[CapabilityScorerExactMatch]
	tool := scorers[CapabilityScorerToolCall]
	packageRef := VersionedRef{ID: BuiltinCapabilityPackageID, SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"}
	dimensions := CapabilityDimensions()
	weights := make(map[CapabilityDimension]float64, len(dimensions))
	tasks := make([]CapabilityTaskDefinition, len(dimensions))
	for index, dimension := range dimensions {
		weights[dimension] = 1 / float64(len(dimensions))
		scorer := exact.Descriptor()
		scorerConfig := json.RawMessage(`{"caseSensitive":false,"trimSpace":true,"collapseWhitespace":true}`)
		if dimension == CapabilityToolUse {
			scorer = tool.Descriptor()
			scorerConfig = json.RawMessage(`{"orderSensitive":true,"allowExtraCalls":false,"requireFinalText":false}`)
		}
		tasks[index] = CapabilityTaskDefinition{
			Ref:       VersionedRef{ID: "capability.task.builtin." + string(dimension), SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
			Dimension: dimension, Difficulty: "core", Visibility: CapabilityTaskParameterized,
			Modalities: []CapabilityModality{CapabilityModalityText},
			Protocols:  []Protocol{ProtocolMessages, ProtocolResponses, ProtocolChat, ProtocolGemini},
			RequestProfiles: []string{
				"messages.standard.v1", "responses.standard.v1", "chat.standard.v1", "gemini.standard.v1",
			},
			Generator:           VersionedRef{ID: "capability.generator.builtin." + string(dimension), SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
			SeedRuleVersion:     "fnv-run-target-task-repetition-v1",
			Scorer:              scorer.Ref,
			ScorerKind:          scorer.Kind,
			ScorerConfig:        scorerConfig,
			ExpectedContract:    json.RawMessage(`{"type":"json_value","reportDisclosure":"excluded"}`),
			MaximumInputTokens:  512,
			MaximumOutputTokens: 128,
			TimeoutMillis:       30_000,
			Repetitions:         3,
			Weight:              1,
			DisclosureRisk:      DisclosureRiskLow,
		}
	}
	taskPackage := CapabilityTaskPackage{
		Ref: packageRef, Description: "七维参数化核心能力任务包；实例答案按运行种子生成并默认不进入 HTML 报告。",
		ScoringModel:     VersionedRef{ID: "capability.aggregate.weighted", SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
		DimensionWeights: weights, Tasks: tasks,
	}
	snapshot, err := NewCapabilityTaskPackageSnapshot(taskPackage)
	if err != nil {
		return nil, err
	}
	presets := make(map[string]CapabilityRunPreset, 3)
	for _, preset := range []CapabilityRunPreset{
		builtinCapabilityPreset(packageRef, CapabilityPresetQuick, 1),
		builtinCapabilityPreset(packageRef, CapabilityPresetStandard, 2),
		builtinCapabilityPreset(packageRef, CapabilityPresetDeep, 3),
	} {
		if _, err := PrepareCapabilityRun(preset, snapshot); err != nil {
			return nil, err
		}
		presets[versionedRefKey(preset.Ref)] = preset
	}
	assets := &BuiltinCapabilityAssets{
		packageSnapshot: snapshot,
		packages:        map[string]CapabilityTaskPackageSnapshot{versionedRefKey(packageRef): snapshot},
		presets:         presets,
		tasks:           make(map[string]CapabilityTaskDefinition, len(tasks)),
		taskPackages:    make(map[string]VersionedRef, len(tasks)),
		questions:       make(map[string]QuestionBankQuestion),
		scorers:         scorers,
		questionBanks:   QuestionBankCatalogSnapshot{Banks: []QuestionBankDescriptor{}, Issues: []QuestionBankLoadIssue{}},
	}
	for _, task := range tasks {
		key := versionedRefKey(task.Ref)
		assets.tasks[key] = task
		assets.taskPackages[key] = packageRef
	}
	return assets, nil
}

func builtinCapabilityScorers() (map[CapabilityScorerKind]CapabilityScorer, error) {
	result := make(map[CapabilityScorerKind]CapabilityScorer)
	for _, kind := range []CapabilityScorerKind{CapabilityScorerExactMatch, CapabilityScorerSetMatch, CapabilityScorerNumericTolerance, CapabilityScorerJSONStructure, CapabilityScorerToolCall, CapabilityScorerAssertions} {
		scorer, err := NewBuiltinCapabilityScorer(kind)
		if err != nil {
			return nil, err
		}
		result[kind] = scorer
	}
	return result, nil
}

func NewQuestionBankCapabilityAssets(sourceRoot, cacheRoot string) (*BuiltinCapabilityAssets, error) {
	assets, err := NewBuiltinCapabilityAssets()
	if err != nil {
		return nil, err
	}
	assets.sourceRoot, assets.cacheRoot = sourceRoot, cacheRoot
	if _, err := assets.ReloadQuestionBanks(); err != nil {
		return nil, err
	}
	return assets, nil
}

func (a *BuiltinCapabilityAssets) ReloadQuestionBanks() (QuestionBankCatalogSnapshot, error) {
	if a == nil || strings.TrimSpace(a.sourceRoot) == "" || strings.TrimSpace(a.cacheRoot) == "" {
		return QuestionBankCatalogSnapshot{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "能力题库目录未配置")
	}
	banks, catalog, err := loadQuestionBanks(a.sourceRoot, a.cacheRoot)
	if err != nil {
		return QuestionBankCatalogSnapshot{}, err
	}
	packages := make(map[string]CapabilityTaskPackageSnapshot)
	presets := make(map[string]CapabilityRunPreset)
	evaluationOptions := make([]CapabilityEvaluationOption, 0)
	tasks := make(map[string]CapabilityTaskDefinition)
	taskPackages := make(map[string]VersionedRef)
	questions := make(map[string]QuestionBankQuestion)
	currentBanks := make([]QuestionBankDescriptor, 0, len(catalog.Banks))
	for _, bank := range banks {
		packageSnapshot, bankPresets, bankOptions, bankTasks, err := buildQuestionBankCapabilityAssets(bank)
		if err != nil {
			catalog.Issues = append(catalog.Issues, QuestionBankLoadIssue{Directory: bank.descriptor.SourcePath, BankID: bank.manifest.ID, Message: err.Error()})
			continue
		}
		packages[versionedRefKey(packageSnapshot.Package.Ref)] = packageSnapshot
		for _, preset := range bankPresets {
			presets[versionedRefKey(preset.Ref)] = preset
		}
		evaluationOptions = append(evaluationOptions, bankOptions...)
		for index, task := range bankTasks {
			key := versionedRefKey(task.Ref)
			tasks[key] = task
			taskPackages[key] = packageSnapshot.Package.Ref
			questions[key] = bank.questions[index]
		}
		if !pathWithinQuestionBankCache(bank.descriptor.SourcePath, a.cacheRoot) {
			currentBanks = append(currentBanks, bank.descriptor)
		}
	}
	if len(packages) == 0 {
		return QuestionBankCatalogSnapshot{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "没有可用的能力题库")
	}
	catalog.Banks = currentBanks
	a.mu.Lock()
	a.packages, a.presets, a.evaluationOptions, a.tasks, a.taskPackages, a.questions, a.questionBanks = packages, presets, evaluationOptions, tasks, taskPackages, questions, catalog
	for _, snapshot := range packages {
		a.packageSnapshot = snapshot
		break
	}
	a.mu.Unlock()
	return catalog, nil
}

func buildQuestionBankCapabilityAssets(bank loadedQuestionBank) (CapabilityTaskPackageSnapshot, []CapabilityRunPreset, []CapabilityEvaluationOption, []CapabilityTaskDefinition, error) {
	implementation := "bank-" + bank.descriptor.ContentSHA256
	packageRef := VersionedRef{ID: "capability.pack.bank." + bank.manifest.ID, SemanticVersion: bank.manifest.Version, ImplementationVersion: implementation}
	tasks := make([]CapabilityTaskDefinition, len(bank.questions))
	for index, question := range bank.questions {
		scorer, err := NewBuiltinCapabilityScorer(question.ScorerKind)
		if err != nil {
			return CapabilityTaskPackageSnapshot{}, nil, nil, nil, err
		}
		maxInput, maxOutput, timeout, weight, risk := question.MaximumInputTokens, question.MaximumOutputTokens, question.TimeoutMillis, question.Weight, question.DisclosureRisk
		if maxInput == 0 {
			maxInput = 512
		}
		if maxOutput == 0 {
			maxOutput = 128
		}
		if timeout == 0 {
			timeout = auditSampleTimeoutMillis
		}
		if weight == 0 {
			weight = 1
		}
		if risk == "" {
			risk = DisclosureRiskLow
		}
		config := question.ScorerConfig
		if len(config) == 0 {
			config = json.RawMessage(`{}`)
		}
		tasks[index] = CapabilityTaskDefinition{
			Ref:       VersionedRef{ID: "capability.task.bank." + bank.manifest.ID + "." + question.ID, SemanticVersion: bank.manifest.Version, ImplementationVersion: implementation},
			Dimension: question.Dimension, Difficulty: question.Difficulty, Visibility: CapabilityTaskPublicFixed,
			Modalities: []CapabilityModality{CapabilityModalityText}, Protocols: []Protocol{ProtocolMessages, ProtocolResponses, ProtocolChat, ProtocolGemini},
			RequestProfiles: []string{"messages.standard.v1", "responses.standard.v1", "chat.standard.v1", "gemini.standard.v1"},
			Generator:       VersionedRef{ID: "capability.generator.bank." + bank.manifest.ID, SemanticVersion: bank.manifest.Version, ImplementationVersion: implementation}, SeedRuleVersion: "question-bank-fixed-v1",
			Scorer: scorer.Descriptor().Ref, ScorerKind: question.ScorerKind, ScorerConfig: config, ExpectedContract: json.RawMessage(`{"type":"json_value","reportDisclosure":"excluded"}`),
			MaximumInputTokens: maxInput, MaximumOutputTokens: maxOutput, TimeoutMillis: timeout, Repetitions: 3, Weight: weight, DisclosureRisk: risk,
		}
	}
	weights := bank.manifest.DimensionWeights
	if len(weights) == 0 {
		weights = make(map[CapabilityDimension]float64)
		for _, dimension := range capabilityDimensions {
			weights[dimension] = 1 / float64(len(capabilityDimensions))
		}
	}
	packageSnapshot, err := NewCapabilityTaskPackageSnapshot(CapabilityTaskPackage{Ref: packageRef, Description: bank.manifest.Description, ScoringModel: VersionedRef{ID: "capability.aggregate.weighted", SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"}, DimensionWeights: weights, Tasks: tasks})
	if err != nil {
		return CapabilityTaskPackageSnapshot{}, nil, nil, nil, err
	}
	presets := make([]CapabilityRunPreset, 0, 11)
	for _, item := range []struct {
		mode        CapabilityPresetMode
		repetitions int
	}{{CapabilityPresetQuick, 1}, {CapabilityPresetStandard, 2}, {CapabilityPresetDeep, 3}} {
		selected := make([]CapabilityPresetTask, len(tasks))
		for i, task := range tasks {
			selected[i] = CapabilityPresetTask{TaskID: task.Ref.ID, Repetitions: item.repetitions}
		}
		limits := capabilityLimitsForTasks(tasks, selected)
		preset := CapabilityRunPreset{Ref: VersionedRef{ID: "capability.preset.bank." + bank.manifest.ID + "." + string(item.mode), SemanticVersion: bank.manifest.Version, ImplementationVersion: implementation}, Mode: item.mode, Package: packageRef, FormalEligible: item.mode != CapabilityPresetQuick, Tasks: selected, Limits: limits}
		if _, err := PrepareCapabilityRun(preset, packageSnapshot); err != nil {
			return CapabilityTaskPackageSnapshot{}, nil, nil, nil, err
		}
		presets = append(presets, preset)
	}

	labels := map[CapabilityDimension]string{
		CapabilityMathLogic: "数学与逻辑", CapabilityCode: "代码", CapabilityInstructionFollowing: "指令遵循",
		CapabilityToolUse: "工具使用", CapabilityLongContextMultiturn: "长上下文与多轮",
		CapabilityKnowledgeFactuality: "知识与事实性", CapabilityRepeatability: "稳定性",
	}
	options := make([]CapabilityEvaluationOption, 0, len(capabilityDimensions)+1)
	addOption := func(suffix, label string, dimension CapabilityDimension) error {
		selected := make([]CapabilityPresetTask, 0, len(tasks))
		for _, task := range tasks {
			if dimension == "" || task.Dimension == dimension {
				selected = append(selected, CapabilityPresetTask{TaskID: task.Ref.ID, Repetitions: 1})
			}
		}
		if len(selected) == 0 {
			return nil
		}
		preset := CapabilityRunPreset{
			Ref:  VersionedRef{ID: "capability.preset.bank." + bank.manifest.ID + "." + suffix, SemanticVersion: bank.manifest.Version, ImplementationVersion: implementation},
			Mode: CapabilityPresetQuick, Package: packageRef, FormalEligible: false, Tasks: selected,
			Limits: capabilityLimitsForTasks(tasks, selected),
		}
		if _, err := PrepareCapabilityRun(preset, packageSnapshot); err != nil {
			return err
		}
		presets = append(presets, preset)
		options = append(options, CapabilityEvaluationOption{
			ID: bank.manifest.ID + "." + suffix, BankID: bank.manifest.ID, BankName: bank.manifest.Name,
			Label: label, Dimension: dimension, QuestionCount: len(selected), Preset: preset.Ref, Limits: preset.Limits,
		})
		return nil
	}
	if err := addOption("all", "综合能力", ""); err != nil {
		return CapabilityTaskPackageSnapshot{}, nil, nil, nil, err
	}
	for _, dimension := range capabilityDimensions {
		if err := addOption(string(dimension), labels[dimension], dimension); err != nil {
			return CapabilityTaskPackageSnapshot{}, nil, nil, nil, err
		}
	}
	return packageSnapshot, presets, options, tasks, nil
}

func capabilityLimitsForTasks(tasks []CapabilityTaskDefinition, selected []CapabilityPresetTask) CapabilityBudgetLimits {
	definitions := make(map[string]CapabilityTaskDefinition, len(tasks))
	for _, task := range tasks {
		definitions[task.Ref.ID] = task
	}
	limits := CapabilityBudgetLimits{}
	for _, selection := range selected {
		task := definitions[selection.TaskID]
		limits.Requests += selection.Repetitions
		limits.InputTokens += int64(task.MaximumInputTokens * selection.Repetitions)
		limits.OutputTokens += int64(task.MaximumOutputTokens * selection.Repetitions)
	}
	limits.TotalTokens = limits.InputTokens + limits.OutputTokens
	return limits
}

func builtinCapabilityPreset(packageRef VersionedRef, mode CapabilityPresetMode, repetitions int) CapabilityRunPreset {
	tasks := make([]CapabilityPresetTask, 0, len(capabilityDimensions))
	for _, dimension := range capabilityDimensions {
		tasks = append(tasks, CapabilityPresetTask{TaskID: "capability.task.builtin." + string(dimension), Repetitions: repetitions})
	}
	requests := len(tasks) * repetitions
	inputTokens := int64(requests * 512)
	outputTokens := int64(requests * 128)
	return CapabilityRunPreset{
		Ref:  VersionedRef{ID: "capability.preset.builtin." + string(mode), SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
		Mode: mode, Package: packageRef, FormalEligible: mode != CapabilityPresetQuick, Tasks: tasks,
		Limits: CapabilityBudgetLimits{Requests: requests, InputTokens: inputTokens, OutputTokens: outputTokens, TotalTokens: inputTokens + outputTokens},
	}
}

func (a *BuiltinCapabilityAssets) Catalog() AuditCapabilityAssetCatalog {
	if a == nil {
		return AuditCapabilityAssetCatalog{Available: false, Reason: "能力资产未初始化", Packages: []CapabilityTaskPackageSnapshot{}, Presets: []CapabilityRunPreset{}}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	currentImplementations := make(map[string]bool)
	for _, bank := range a.questionBanks.Banks {
		currentImplementations["bank-"+bank.ContentSHA256] = true
	}
	presetIDs := make([]string, 0, len(a.presets))
	for id, preset := range a.presets {
		if strings.HasPrefix(preset.Ref.ImplementationVersion, "bank-") && !currentImplementations[preset.Ref.ImplementationVersion] {
			continue
		}
		presetIDs = append(presetIDs, id)
	}
	// 按 mode 语义顺序排序（quick→standard→deep），而非 ID 字符串排序，
	// 确保目录展示顺序稳定且符合用户认知。
	sort.Slice(presetIDs, func(i, j int) bool {
		return capabilityPresetModeOrder(a.presets[presetIDs[i]].Mode) < capabilityPresetModeOrder(a.presets[presetIDs[j]].Mode)
	})
	presets := make([]CapabilityRunPreset, 0, len(a.presets))
	for _, id := range presetIDs {
		preset := a.presets[id]
		preset.Tasks = append([]CapabilityPresetTask(nil), preset.Tasks...)
		presets = append(presets, preset)
	}
	evaluationOptions := make([]CapabilityEvaluationOption, 0, len(a.evaluationOptions))
	for _, option := range a.evaluationOptions {
		if currentImplementations[option.Preset.ImplementationVersion] {
			evaluationOptions = append(evaluationOptions, option)
		}
	}
	sort.Slice(evaluationOptions, func(i, j int) bool {
		if evaluationOptions[i].BankName != evaluationOptions[j].BankName {
			return evaluationOptions[i].BankName < evaluationOptions[j].BankName
		}
		return evaluationOptions[i].ID < evaluationOptions[j].ID
	})
	packageKeys := make([]string, 0, len(a.packages))
	for key := range a.packages {
		packageKeys = append(packageKeys, key)
	}
	sort.Strings(packageKeys)
	packages := make([]CapabilityTaskPackageSnapshot, 0, len(packageKeys))
	for _, key := range packageKeys {
		packages = append(packages, a.packages[key])
	}
	return AuditCapabilityAssetCatalog{
		Available:         true,
		Packages:          packages,
		Presets:           presets,
		EvaluationOptions: evaluationOptions,
	}
}

func (a *BuiltinCapabilityAssets) ResolvePreset(reference VersionedRef) (CapabilityRunPreset, CapabilityRunPlan, bool, error) {
	if a == nil {
		return CapabilityRunPreset{}, CapabilityRunPlan{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "能力资产未初始化")
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	preset, found := a.presets[versionedRefKey(reference)]
	if !found {
		return CapabilityRunPreset{}, CapabilityRunPlan{}, false, nil
	}
	packageSnapshot, ok := a.packages[versionedRefKey(preset.Package)]
	if !ok {
		return CapabilityRunPreset{}, CapabilityRunPlan{}, false, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "能力预设引用的题库快照不可用")
	}
	plan, err := PrepareCapabilityRun(preset, packageSnapshot)
	return preset, plan, true, err
}

func (a *BuiltinCapabilityAssets) Task(reference VersionedRef) (CapabilityTaskDefinition, bool) {
	if a == nil {
		return CapabilityTaskDefinition{}, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	task, found := a.tasks[versionedRefKey(reference)]
	return task, found
}

func (a *BuiltinCapabilityAssets) PackageForTask(taskRef VersionedRef) (CapabilityTaskPackageSnapshot, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	ref, found := a.taskPackages[versionedRefKey(taskRef)]
	if !found {
		return CapabilityTaskPackageSnapshot{}, false
	}
	snapshot, found := a.packages[versionedRefKey(ref)]
	return snapshot, found
}

func (a *BuiltinCapabilityAssets) QuestionBankCatalog() QuestionBankCatalogSnapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.questionBanks
}

func (a *BuiltinCapabilityAssets) Score(
	ctx context.Context,
	task CapabilityTaskDefinition,
	instance CapabilityTaskInstance,
	execution ExecutionResult,
	evidence []EvidenceReference,
) (CapabilityScore, error) {
	if a == nil {
		return CapabilityScore{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "能力资产未初始化")
	}
	scorer := a.scorers[task.ScorerKind]
	if scorer == nil {
		return CapabilityScore{}, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "能力任务引用的判分器不可用")
	}
	return scorer.Score(ctx, CapabilityScoringInput{
		Task: task, Instance: instance, ExpectedOutput: instance.ExpectedOutput,
		Observed: CapabilityObservedOutput{Text: execution.Text, StructuredOutput: execution.StructuredOutput, ToolCalls: execution.ToolCalls},
		Evidence: evidence,
	})
}

func (a *BuiltinCapabilityAssets) GenerateInstance(
	runID string,
	target TargetSnapshot,
	task CapabilityTaskDefinition,
	repetition int,
	seed uint64,
	createdAt time.Time,
) (CapabilityTaskInstance, error) {
	if a == nil {
		return CapabilityTaskInstance{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "能力资产未初始化")
	}
	if !capabilityTaskSupportsTarget(task, target) {
		return CapabilityTaskInstance{}, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "能力任务不支持当前协议、请求轮廓或思考档位")
	}
	a.mu.RLock()
	key := versionedRefKey(task.Ref)
	question, fromBank := a.questions[key]
	packageRef := a.taskPackages[key]
	a.mu.RUnlock()
	var prompt string
	var expected json.RawMessage
	var tools []ToolDefinition
	var err error
	if fromBank {
		prompt, expected, tools = question.Prompt, append(json.RawMessage(nil), question.ExpectedOutput...), append([]ToolDefinition(nil), question.Tools...)
	} else {
		prompt, expected, tools, err = generateBuiltinCapabilityInput(task.Dimension, seed)
	}
	if err != nil {
		return CapabilityTaskInstance{}, err
	}
	features := RequestFeatures{Tools: len(tools) > 0}
	execution := ExecutionSpec{
		Purpose: PurposeCapabilityEval, Target: ChannelTarget{ChannelID: target.ChannelID, ChannelKind: target.ChannelKind},
		Protocol: target.Protocol, Model: target.RequestedModel, Thinking: target.Thinking, RequestProfile: target.RequestProfile,
		Stream: false, TimeoutMillis: task.TimeoutMillis, MaxOutputTokens: task.MaximumOutputTokens,
		Features: features, Input: ExecutionInput{Prompt: prompt, Tools: tools}, Redaction: RedactionDigest,
	}
	inputBytes, err := json.Marshal(execution.Input)
	if err != nil {
		return CapabilityTaskInstance{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码能力任务输入失败", err)
	}
	digest := sha256.Sum256(inputBytes)
	instance := CapabilityTaskInstance{
		InstanceID: runID + "." + target.ChannelID + "." + task.Ref.ID + "." + strconv.Itoa(repetition),
		Package:    packageRef, Task: task.Ref, Dimension: task.Dimension, Seed: seed, Repetition: repetition,
		InputSHA256: hex.EncodeToString(digest[:]), ExpectedOutput: expected, Execution: execution, CreatedAt: createdAt,
	}
	if err := instance.Validate(); err != nil {
		return CapabilityTaskInstance{}, err
	}
	return instance, nil
}

func capabilityTaskSupportsTarget(task CapabilityTaskDefinition, target TargetSnapshot) bool {
	protocolSupported := false
	for _, protocol := range task.Protocols {
		protocolSupported = protocolSupported || protocol == target.Protocol
	}
	profileSupported := false
	for _, profile := range task.RequestProfiles {
		profileSupported = profileSupported || profile == target.RequestProfile
	}
	thinkingSupported := len(task.ThinkingLevels) == 0
	for _, thinking := range task.ThinkingLevels {
		thinkingSupported = thinkingSupported || thinking == target.Thinking
	}
	return protocolSupported && profileSupported && thinkingSupported
}

func generateBuiltinCapabilityInput(
	dimension CapabilityDimension,
	seed uint64,
) (string, json.RawMessage, []ToolDefinition, error) {
	left := int(seed%37) + 11
	right := int((seed/37)%29) + 7
	var prompt string
	var expected any
	var tools []ToolDefinition
	switch dimension {
	case CapabilityMathLogic:
		prompt = fmt.Sprintf("只输出十进制整数，不要解释：(%d * %d) + %d = ?", left, right, left)
		expected = strconv.Itoa(left*right + left)
	case CapabilityCode:
		prompt = fmt.Sprintf("阅读伪代码并只输出最终整数：x=%d; repeat %d times { x=x+3 }; output x", left, right%5+2)
		expected = strconv.Itoa(left + (right%5+2)*3)
	case CapabilityInstructionFollowing:
		token := fmt.Sprintf("AUDIT-%d-%d", left, right)
		prompt = "严格只输出下面引号内的内容，不要引号、标点或解释：\"" + token + "\""
		expected = token
	case CapabilityToolUse:
		recordID := left*100 + right
		prompt = fmt.Sprintf("必须调用 lookup_audit_record 工具查询记录 %d；不要猜测结果，也不要输出普通文本。", recordID)
		arguments, _ := json.Marshal(map[string]int{"id": recordID})
		expected = struct {
			Calls []ToolCall `json:"calls"`
		}{Calls: []ToolCall{{Name: "lookup_audit_record", Arguments: arguments}}}
		tools = []ToolDefinition{{
			Name: "lookup_audit_record", Description: "按 ID 查询固定审计记录",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"],"additionalProperties":false}`),
		}}
	case CapabilityLongContextMultiturn:
		needle := fmt.Sprintf("K%d-%d", left, right)
		prompt = fmt.Sprintf("材料：A=蓝色；B=17；C=%s；D=周四；E=封存。忽略常识，只根据材料回答。只输出 C 的值。", needle)
		expected = needle
	case CapabilityKnowledgeFactuality:
		prompt = "仅依据给定材料回答，不使用外部知识。材料：虚构行星 Arin 的卫星叫 Noma。问题：Arin 的卫星叫什么？只输出名称。"
		expected = "Noma"
	case CapabilityRepeatability:
		prompt = "只输出十进制整数，不要解释：17 * 19 = ?"
		expected = "323"
	default:
		return "", nil, nil, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "能力维度没有内置生成器")
	}
	encodedExpected, err := json.Marshal(expected)
	if err != nil {
		return "", nil, nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码能力任务期望输出失败", err)
	}
	return prompt, encodedExpected, tools, nil
}
