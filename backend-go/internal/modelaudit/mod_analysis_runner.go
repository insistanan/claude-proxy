package modelaudit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var ErrAuditModAnalysisBudgetExceeded = errors.New("Mod LLM 分析超出审计运行预算")

var auditModArtifactSegmentPattern = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

type AuditModAnalysisRunnerConfig struct {
	ArtifactRoot     string
	PythonExecutable string
	LLMTimeout       time.Duration
	LLMMaxTokens     int
}

func DefaultAuditModAnalysisRunnerConfig() AuditModAnalysisRunnerConfig {
	return AuditModAnalysisRunnerConfig{
		ArtifactRoot: ".config/model-audit/artifacts", LLMTimeout: 2 * time.Minute, LLMMaxTokens: 2048,
	}
}

func (c AuditModAnalysisRunnerConfig) validate() error {
	if strings.TrimSpace(c.ArtifactRoot) == "" || c.LLMTimeout <= 0 || c.LLMMaxTokens <= 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "Mod 分析执行器配置无效")
	}
	return nil
}

type AuditModAnalysisRunner struct {
	store   *AuditSQLiteStore
	service *Service
	config  AuditModAnalysisRunnerConfig
	now     func() time.Time
	newID   func(string) (string, error)
}

func NewAuditModAnalysisRunner(store *AuditSQLiteStore, service *Service, config AuditModAnalysisRunnerConfig) (*AuditModAnalysisRunner, error) {
	if store == nil || service == nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "Mod 分析执行器依赖未初始化")
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	absoluteRoot, err := filepath.Abs(config.ArtifactRoot)
	if err != nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "解析 Mod 分析产物目录失败", err)
	}
	config.ArtifactRoot = absoluteRoot
	return &AuditModAnalysisRunner{store: store, service: service, config: config, now: time.Now, newID: NewAuditEntityID}, nil
}

func (r *AuditModAnalysisRunner) Run(
	ctx context.Context,
	job AuditJob,
	run AuditRun,
	targetID string,
	target TargetSnapshot,
	bundle AuditModBundle,
	samples []StrategySample,
	features AuditModEvaluationFeatures,
) (AuditModAnalysisRecord, AuditRunUsage, error) {
	input, err := buildAuditModAnalysisInput(run, targetID, target, bundle.Reference, samples, features)
	if err != nil {
		return AuditModAnalysisRecord{}, AuditRunUsage{}, err
	}
	return r.runInput(ctx, job, run, targetID, bundle, input, true)
}

func (r *AuditModAnalysisRunner) Rerun(
	ctx context.Context,
	job AuditJob,
	run AuditRun,
	targetID string,
	bundle AuditModBundle,
	input json.RawMessage,
) (AuditModAnalysisRecord, error) {
	canonical, _, err := canonicalJSONObject(input)
	if err != nil {
		return AuditModAnalysisRecord{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "重新分析输入无效", err)
	}
	var decoded AuditModAnalysisInput
	if err := json.Unmarshal(canonical, &decoded); err != nil || decoded.Schema != AuditModAnalysisInputSchema || decoded.RunID != run.ID ||
		decoded.JobID != job.ID || decoded.TargetID != targetID || decoded.Mod != bundle.Reference {
		return AuditModAnalysisRecord{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "重新分析输入与冻结运行或 Mod 版本不一致", err)
	}
	record, _, err := r.runInput(ctx, job, run, targetID, bundle, canonical, false)
	return record, err
}

func (r *AuditModAnalysisRunner) runInput(
	ctx context.Context,
	job AuditJob,
	run AuditRun,
	targetID string,
	bundle AuditModBundle,
	input json.RawMessage,
	enforceRunBudget bool,
) (AuditModAnalysisRecord, AuditRunUsage, error) {
	if enforceRunBudget && bundle.Manifest.Analysis.Mode == AuditModAnalysisLLM {
		budget, err := r.llmRequestBudget(bundle, input)
		if err != nil {
			return AuditModAnalysisRecord{}, AuditRunUsage{}, err
		}
		decision, err := CheckAuditRunBudget(run, budget)
		if err != nil {
			return AuditModAnalysisRecord{}, AuditRunUsage{}, err
		}
		if !decision.Allowed {
			return AuditModAnalysisRecord{}, AuditRunUsage{}, ErrAuditModAnalysisBudgetExceeded
		}
	}
	analysisID, err := r.newID("audit-mod-analysis")
	if err != nil {
		return AuditModAnalysisRecord{}, AuditRunUsage{}, err
	}
	workingDirectory := filepath.Join(r.config.ArtifactRoot, run.ID, auditModArtifactSegment(targetID), bundle.Manifest.ID, analysisID)
	if err := os.MkdirAll(workingDirectory, 0755); err != nil {
		return AuditModAnalysisRecord{}, AuditRunUsage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "创建 Mod 分析运行目录失败", err)
	}
	inputPath := filepath.Join(workingDirectory, "analysis-input.json")
	outputPath := filepath.Join(workingDirectory, "analysis-output.json")
	if err := os.WriteFile(inputPath, input, 0644); err != nil {
		return AuditModAnalysisRecord{}, AuditRunUsage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "写入 Mod 分析输入失败", err)
	}
	record, err := NewAuditModAnalysisRecord(analysisID, run.ID, targetID, bundle.Reference, bundle.Manifest.Analysis.Mode, input, r.now().UTC())
	if err != nil {
		return AuditModAnalysisRecord{}, AuditRunUsage{}, err
	}
	methodPath, _ := normalizeAuditModRelativePath(bundle.Manifest.Analysis.MethodFile, "分析方法")
	record.SourceDirectory = bundle.SourcePath
	record.SnapshotDirectory = bundle.SnapshotPath
	record.Method = strings.TrimSpace(string(bundle.Files[methodPath]))
	record.WorkingDirectory = workingDirectory
	if bundle.Manifest.Analysis.Mode == AuditModAnalysisLLM {
		if job.Analyzer == nil {
			return AuditModAnalysisRecord{}, AuditRunUsage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "LLM Mod 缺少任务级分析模型")
		}
		record.Analyzer = &AuditModAnalyzerTarget{
			ChannelID: job.Analyzer.ChannelID, ChannelKind: job.Analyzer.ChannelKind, Model: job.Analyzer.Model,
			Protocol: job.Analyzer.Protocol, Thinking: job.Analyzer.Thinking, RequestProfile: job.Analyzer.RequestProfile,
		}
	}
	if err := r.store.CreateModAnalysis(ctx, record); err != nil {
		return AuditModAnalysisRecord{}, AuditRunUsage{}, err
	}
	switch bundle.Manifest.Analysis.Mode {
	case AuditModAnalysisLLM:
		return r.runLLM(ctx, record, *job.Analyzer, bundle)
	case AuditModAnalysisPython:
		return r.runPython(ctx, record, bundle, inputPath, outputPath)
	case AuditModAnalysisManual:
		return r.prepareManual(ctx, record, bundle, inputPath, outputPath)
	default:
		return AuditModAnalysisRecord{}, AuditRunUsage{}, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "Mod 分析模式不可用")
	}
}

func auditModArtifactSegment(value string) string {
	digest := sha256.Sum256([]byte(value))
	base := strings.Trim(auditModArtifactSegmentPattern.ReplaceAllString(value, "_"), "._-")
	if base == "" {
		base = "target"
	}
	if len(base) > 48 {
		base = base[:48]
	}
	return base + "-" + hex.EncodeToString(digest[:6])
}

func (r *AuditModAnalysisRunner) runLLM(
	ctx context.Context,
	record AuditModAnalysisRecord,
	target AuditAnalysisTarget,
	bundle AuditModBundle,
) (AuditModAnalysisRecord, AuditRunUsage, error) {
	promptPath, _ := normalizeAuditModRelativePath(bundle.Manifest.Analysis.PromptFile, "分析提示词")
	promptTemplate := string(bundle.Files[promptPath])
	if !strings.Contains(promptTemplate, "{{analysis_data_json}}") {
		failed, err := r.fail(ctx, record, "analysis-prompt.md 缺少 {{analysis_data_json}} 占位符", nil)
		return failed, AuditRunUsage{}, err
	}
	prompt := strings.ReplaceAll(promptTemplate, "{{analysis_data_json}}", string(record.Input))
	spec := ExecutionSpec{
		Purpose: PurposeAuditAnalysis, Target: ChannelTarget{ChannelID: target.ChannelID, ChannelKind: target.ChannelKind},
		Protocol: target.Protocol, Model: target.Model, Thinking: target.Thinking, RequestProfile: target.RequestProfile,
		Stream: false, TimeoutMillis: r.config.LLMTimeout.Milliseconds(), MaxOutputTokens: r.config.LLMMaxTokens,
		Input: ExecutionInput{Prompt: prompt}, Redaction: RedactionDigest,
	}
	resolved, err := r.service.ResolveTarget(ctx, spec)
	if err != nil {
		failed, storeErr := r.fail(ctx, record, "解析 Mod LLM 分析目标失败", err)
		return failed, AuditRunUsage{}, storeErr
	}
	running, err := r.start(ctx, record, nil, "", "")
	if err != nil {
		return AuditModAnalysisRecord{}, AuditRunUsage{}, err
	}
	execution, executeErr := r.service.ExecuteResolved(ctx, spec, resolved)
	if executeErr != nil {
		failed, storeErr := r.fail(ctx, running, "调用 Mod LLM 分析模型失败", executeErr)
		return failed, AuditRunUsage{}, storeErr
	}
	usage := AuditRunUsage{
		Requests: 1, InputTokens: int64(execution.Usage.InputTokens), OutputTokens: int64(execution.Usage.OutputTokens),
		TotalTokens: int64(execution.Usage.InputTokens + execution.Usage.OutputTokens),
	}
	running.Usage = usage
	raw := []byte(strings.TrimSpace(execution.Text))
	if len(execution.StructuredOutput) > 0 {
		raw = execution.StructuredOutput
	}
	if execution.Status != StatusCompleted {
		failed, storeErr := r.fail(ctx, running, "Mod LLM 分析请求未成功完成", nil)
		return failed, usage, storeErr
	}
	completed, err := r.complete(ctx, running, raw, execution.Text)
	return completed, usage, err
}

func (r *AuditModAnalysisRunner) llmRequestBudget(bundle AuditModBundle, input json.RawMessage) (AuditRequestBudget, error) {
	promptPath, err := normalizeAuditModRelativePath(bundle.Manifest.Analysis.PromptFile, "分析提示词")
	if err != nil {
		return AuditRequestBudget{}, err
	}
	promptTemplate, found := bundle.Files[promptPath]
	if !found || !strings.Contains(string(promptTemplate), "{{analysis_data_json}}") {
		return AuditRequestBudget{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "analysis-prompt.md 缺少 {{analysis_data_json}} 占位符")
	}
	promptLength := len(strings.ReplaceAll(string(promptTemplate), "{{analysis_data_json}}", string(input)))
	return AuditRequestBudget{MaximumInputTokens: int64(maxInt(1, promptLength)), MaximumOutputTokens: int64(r.config.LLMMaxTokens)}, nil
}

func (r *AuditModAnalysisRunner) runPython(
	ctx context.Context,
	record AuditModAnalysisRecord,
	bundle AuditModBundle,
	inputPath string,
	outputPath string,
) (AuditModAnalysisRecord, AuditRunUsage, error) {
	python := bundle.Manifest.Analysis.Python
	executable, prefix, err := r.resolvePython(python.Interpreter)
	if err != nil {
		failed, storeErr := r.fail(ctx, record, "找不到可用的 Python 解释器", err)
		return failed, AuditRunUsage{}, storeErr
	}
	entryPath, _ := normalizeAuditModRelativePath(python.Entry, "Python 入口")
	entryPath = filepath.Join(bundle.SnapshotPath, filepath.FromSlash(entryPath))
	arguments := make([]string, 0)
	if len(python.Arguments) == 0 {
		arguments = []string{"--input", inputPath, "--output", outputPath}
	} else {
		for _, argument := range python.Arguments {
			argument = strings.ReplaceAll(argument, "{input}", inputPath)
			argument = strings.ReplaceAll(argument, "{output}", outputPath)
			arguments = append(arguments, argument)
		}
	}
	command := append(append(append([]string(nil), prefix...), entryPath), arguments...)
	version := r.pythonVersion(ctx, executable, prefix)
	running, err := r.start(ctx, record, append([]string{executable}, command...), executable, version)
	if err != nil {
		return AuditModAnalysisRecord{}, AuditRunUsage{}, err
	}
	timeout := time.Duration(python.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	processContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(processContext, executable, command...)
	cmd.Dir = bundle.SnapshotPath
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	running.Stdout = stdout.String()
	running.Stderr = stderr.String()
	if cmd.ProcessState != nil {
		exitCode := cmd.ProcessState.ExitCode()
		running.ExitCode = &exitCode
	}
	if runErr != nil {
		message := "Python Mod 分析执行失败"
		if errors.Is(processContext.Err(), context.DeadlineExceeded) {
			message = "Python Mod 分析执行超时"
		}
		failed, storeErr := r.fail(ctx, running, message, runErr)
		return failed, AuditRunUsage{}, storeErr
	}
	output, err := os.ReadFile(outputPath)
	if err != nil {
		failed, storeErr := r.fail(ctx, running, "Python Mod 分析没有生成 analysis-output.json", err)
		return failed, AuditRunUsage{}, storeErr
	}
	completed, err := r.complete(ctx, running, output, "")
	return completed, AuditRunUsage{}, err
}

func (r *AuditModAnalysisRunner) prepareManual(
	ctx context.Context,
	record AuditModAnalysisRecord,
	bundle AuditModBundle,
	inputPath string,
	outputPath string,
) (AuditModAnalysisRecord, AuditRunUsage, error) {
	command := strings.ReplaceAll(bundle.Manifest.Analysis.Manual.CommandExample, "{input}", inputPath)
	command = strings.ReplaceAll(command, "{output}", outputPath)
	next := record
	next.Revision++
	next.Status = AuditModAnalysisAwaitingManual
	next.Command = []string{command}
	if err := r.store.UpdateModAnalysis(ctx, next, record.Revision); err != nil {
		return AuditModAnalysisRecord{}, AuditRunUsage{}, err
	}
	return next, AuditRunUsage{}, nil
}

func (r *AuditModAnalysisRunner) ImportManualResult(ctx context.Context, analysisID string, output json.RawMessage) (AuditModAnalysisRecord, error) {
	record, found, err := r.store.GetModAnalysis(ctx, analysisID)
	if err != nil {
		return AuditModAnalysisRecord{}, err
	}
	if !found {
		return AuditModAnalysisRecord{}, auditNotFound("Mod 分析", analysisID)
	}
	if record.Mode != AuditModAnalysisManual || record.Status != AuditModAnalysisAwaitingManual {
		return AuditModAnalysisRecord{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "Mod 分析不在等待手动结果状态")
	}
	if err := os.WriteFile(filepath.Join(record.WorkingDirectory, "analysis-output.json"), output, 0644); err != nil {
		return AuditModAnalysisRecord{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "写入手动 Mod 分析结果失败", err)
	}
	return r.complete(ctx, record, output, "")
}

func (r *AuditModAnalysisRunner) start(
	ctx context.Context,
	record AuditModAnalysisRecord,
	command []string,
	interpreter string,
	interpreterVersion string,
) (AuditModAnalysisRecord, error) {
	now := r.now().UTC()
	next := record
	next.Revision++
	next.Status = AuditModAnalysisRunning
	next.StartedAt = &now
	next.Command = append([]string(nil), command...)
	next.Interpreter = interpreter
	next.InterpreterVersion = interpreterVersion
	if err := r.store.UpdateModAnalysis(ctx, next, record.Revision); err != nil {
		return AuditModAnalysisRecord{}, err
	}
	return next, nil
}

func (r *AuditModAnalysisRunner) complete(
	ctx context.Context,
	record AuditModAnalysisRecord,
	raw json.RawMessage,
	rawResponse string,
) (AuditModAnalysisRecord, error) {
	canonical, _, err := decodeAuditModAnalysisResult(raw)
	if err != nil {
		return r.fail(ctx, record, "Mod 分析输出合同无效", err)
	}
	canonical, digest, err := canonicalJSONObject(canonical)
	if err != nil {
		return r.fail(ctx, record, "规范化 Mod 分析输出失败", err)
	}
	now := r.now().UTC()
	next := record
	next.Revision++
	next.Status = AuditModAnalysisCompleted
	next.Result = canonical
	next.ResultSHA256 = digest
	next.RawResponse = rawResponse
	next.FinishedAt = &now
	next.Failure = ""
	if err := r.store.UpdateModAnalysis(ctx, next, record.Revision); err != nil {
		return AuditModAnalysisRecord{}, err
	}
	return next, nil
}

func (r *AuditModAnalysisRunner) fail(
	ctx context.Context,
	record AuditModAnalysisRecord,
	message string,
	cause error,
) (AuditModAnalysisRecord, error) {
	now := r.now().UTC()
	next := record
	next.Revision++
	next.Status = AuditModAnalysisFailed
	next.FinishedAt = &now
	next.Result = nil
	next.ResultSHA256 = ""
	next.Failure = message
	if cause != nil {
		next.Failure += ": " + cause.Error()
	}
	if err := r.store.UpdateModAnalysis(ctx, next, record.Revision); err != nil {
		return AuditModAnalysisRecord{}, err
	}
	return next, nil
}

func (r *AuditModAnalysisRunner) resolvePython(manifestValue string) (string, []string, error) {
	if value := strings.TrimSpace(manifestValue); value != "" && !strings.EqualFold(value, "auto") {
		path, err := exec.LookPath(value)
		return path, nil, err
	}
	if value := strings.TrimSpace(r.config.PythonExecutable); value != "" {
		path, err := exec.LookPath(value)
		return path, nil, err
	}
	for _, candidate := range []struct {
		name   string
		prefix []string
	}{{name: "py", prefix: []string{"-3"}}, {name: "python"}, {name: "python3"}} {
		path, err := exec.LookPath(candidate.name)
		if err == nil {
			return path, candidate.prefix, nil
		}
	}
	return "", nil, fmt.Errorf("未发现 py、python 或 python3")
}

func (r *AuditModAnalysisRunner) pythonVersion(ctx context.Context, executable string, prefix []string) string {
	arguments := append(append([]string(nil), prefix...), "--version")
	versionContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(versionContext, executable, arguments...).CombinedOutput()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}
