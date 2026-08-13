package modelaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

type AuditModStoredFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type AuditModStoredVersion struct {
	Reference    AuditModReference    `json:"reference"`
	Manifest     AuditModManifest     `json:"manifest"`
	AnalysisMode AuditModAnalysisMode `json:"analysisMode"`
	SourcePath   string               `json:"sourcePath"`
	SnapshotPath string               `json:"snapshotPath"`
	Files        []AuditModStoredFile `json:"files"`
	LoadedAt     time.Time            `json:"loadedAt"`
}

func NewAuditModStoredVersion(bundle AuditModBundle) (AuditModStoredVersion, error) {
	files := make([]AuditModStoredFile, 0, len(bundle.Files))
	for path, content := range bundle.Files {
		digest := sha256.Sum256(content)
		files = append(files, AuditModStoredFile{Path: path, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(content))})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	version := AuditModStoredVersion{
		Reference: bundle.Reference, Manifest: bundle.Manifest, AnalysisMode: bundle.Manifest.Analysis.Mode,
		SourcePath: bundle.SourcePath, SnapshotPath: bundle.SnapshotPath, Files: files, LoadedAt: bundle.LoadedAt,
	}
	if err := version.Validate(); err != nil {
		return AuditModStoredVersion{}, err
	}
	return version, nil
}

func (v AuditModStoredVersion) Validate() error {
	if err := v.Reference.Validate(); err != nil {
		return err
	}
	if err := v.Manifest.Validate(); err != nil {
		return err
	}
	if v.Manifest.ID != v.Reference.ID || v.Manifest.Version != v.Reference.Version || v.AnalysisMode != v.Manifest.Analysis.Mode ||
		strings.TrimSpace(v.SourcePath) == "" || strings.TrimSpace(v.SnapshotPath) == "" || v.LoadedAt.IsZero() || len(v.Files) == 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 存储版本元数据无效")
	}
	seen := make(map[string]struct{}, len(v.Files))
	for _, file := range v.Files {
		path, err := normalizeAuditModRelativePath(file.Path, "Mod 存储文件")
		if err != nil || path != file.Path || !validSHA256(file.SHA256) || file.Size < 0 {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 存储文件元数据无效", err)
		}
		if _, duplicate := seen[path]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 存储文件重复")
		}
		seen[path] = struct{}{}
	}
	return nil
}

type AuditModAnalysisStatus string

const (
	AuditModAnalysisPending        AuditModAnalysisStatus = "pending"
	AuditModAnalysisRunning        AuditModAnalysisStatus = "running"
	AuditModAnalysisAwaitingManual AuditModAnalysisStatus = "awaiting_manual"
	AuditModAnalysisCompleted      AuditModAnalysisStatus = "completed"
	AuditModAnalysisFailed         AuditModAnalysisStatus = "failed"
)

func (s AuditModAnalysisStatus) Valid() bool {
	return s == AuditModAnalysisPending || s == AuditModAnalysisRunning || s == AuditModAnalysisAwaitingManual ||
		s == AuditModAnalysisCompleted || s == AuditModAnalysisFailed
}

func (s AuditModAnalysisStatus) Terminal() bool {
	return s == AuditModAnalysisCompleted || s == AuditModAnalysisFailed
}

func validAuditModAnalysisTransition(from, to AuditModAnalysisStatus) bool {
	switch from {
	case AuditModAnalysisPending:
		return to == AuditModAnalysisRunning || to == AuditModAnalysisAwaitingManual || to == AuditModAnalysisFailed
	case AuditModAnalysisRunning:
		return to == AuditModAnalysisCompleted || to == AuditModAnalysisFailed
	case AuditModAnalysisAwaitingManual:
		return to == AuditModAnalysisCompleted || to == AuditModAnalysisFailed
	default:
		return false
	}
}

type AuditModAnalyzerTarget struct {
	ChannelID      string        `json:"channelId"`
	ChannelKind    ChannelKind   `json:"channelKind"`
	Model          string        `json:"model"`
	Protocol       Protocol      `json:"protocol"`
	Thinking       ThinkingLevel `json:"thinking"`
	RequestProfile string        `json:"requestProfile"`
}

func (t AuditModAnalyzerTarget) Validate() error {
	if strings.TrimSpace(t.ChannelID) == "" || !t.ChannelKind.Valid() || strings.TrimSpace(t.Model) == "" ||
		!t.Protocol.Valid() || t.Protocol.ChannelKind() != t.ChannelKind || !t.Thinking.Valid() || !stableIDPattern.MatchString(t.RequestProfile) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod LLM 分析目标无效")
	}
	return nil
}

type AuditModAnalysisRecord struct {
	ID                 string                  `json:"id"`
	Revision           uint64                  `json:"revision"`
	RunID              string                  `json:"runId"`
	TargetID           string                  `json:"targetId"`
	Mod                AuditModReference       `json:"mod"`
	Mode               AuditModAnalysisMode    `json:"mode"`
	Status             AuditModAnalysisStatus  `json:"status"`
	Analyzer           *AuditModAnalyzerTarget `json:"analyzer,omitempty"`
	SourceDirectory    string                  `json:"sourceDirectory,omitempty"`
	SnapshotDirectory  string                  `json:"snapshotDirectory,omitempty"`
	WorkingDirectory   string                  `json:"workingDirectory,omitempty"`
	Method             string                  `json:"method,omitempty"`
	Interpreter        string                  `json:"interpreter,omitempty"`
	InterpreterVersion string                  `json:"interpreterVersion,omitempty"`
	Command            []string                `json:"command,omitempty"`
	ExitCode           *int                    `json:"exitCode,omitempty"`
	Stdout             string                  `json:"stdout,omitempty"`
	Stderr             string                  `json:"stderr,omitempty"`
	Input              json.RawMessage         `json:"input"`
	InputSHA256        string                  `json:"inputSha256"`
	RawResponse        string                  `json:"rawResponse,omitempty"`
	Usage              AuditRunUsage           `json:"usage"`
	Result             json.RawMessage         `json:"result,omitempty"`
	ResultSHA256       string                  `json:"resultSha256,omitempty"`
	Failure            string                  `json:"failure,omitempty"`
	CreatedAt          time.Time               `json:"createdAt"`
	StartedAt          *time.Time              `json:"startedAt,omitempty"`
	FinishedAt         *time.Time              `json:"finishedAt,omitempty"`
}

func NewAuditModAnalysisRecord(
	id string,
	runID string,
	targetID string,
	mod AuditModReference,
	mode AuditModAnalysisMode,
	input json.RawMessage,
	createdAt time.Time,
) (AuditModAnalysisRecord, error) {
	canonical, digest, err := canonicalJSONObject(input)
	if err != nil {
		return AuditModAnalysisRecord{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 分析输入必须是 JSON 对象", err)
	}
	record := AuditModAnalysisRecord{
		ID: id, RunID: runID, TargetID: targetID, Mod: mod, Mode: mode, Status: AuditModAnalysisPending,
		Input: canonical, InputSHA256: digest, CreatedAt: createdAt.UTC(),
	}
	if err := record.Validate(); err != nil {
		return AuditModAnalysisRecord{}, err
	}
	return record, nil
}

func (r AuditModAnalysisRecord) Validate() error {
	if !validAuditEntityID(r.ID) || !validAuditEntityID(r.RunID) || strings.TrimSpace(r.TargetID) == "" ||
		r.Revision == ^uint64(0) || !r.Mode.Valid() || !r.Status.Valid() || r.CreatedAt.IsZero() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 分析记录标识、模式、状态或时间无效")
	}
	if err := r.Mod.Validate(); err != nil {
		return err
	}
	canonicalInput, inputDigest, err := canonicalJSONObject(r.Input)
	if err != nil || string(canonicalInput) != string(r.Input) || inputDigest != r.InputSHA256 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 分析输入或哈希无效", err)
	}
	if r.Analyzer != nil {
		if r.Mode != AuditModAnalysisLLM {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "非 LLM 分析不能绑定分析模型")
		}
		if err := r.Analyzer.Validate(); err != nil {
			return err
		}
	}
	if len(r.Command) > 0 {
		for _, argument := range r.Command {
			if argument == "" {
				return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 分析命令包含空参数")
			}
		}
	}
	if err := r.Usage.Validate(); err != nil {
		return err
	}
	switch r.Status {
	case AuditModAnalysisPending:
		if r.StartedAt != nil || r.FinishedAt != nil || len(r.Result) > 0 || r.Failure != "" {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "待执行 Mod 分析包含运行或结果字段")
		}
	case AuditModAnalysisRunning:
		if r.StartedAt == nil || r.StartedAt.IsZero() || r.FinishedAt != nil || len(r.Result) > 0 || r.Failure != "" {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "运行中 Mod 分析状态不完整")
		}
	case AuditModAnalysisAwaitingManual:
		if r.Mode != AuditModAnalysisManual || strings.TrimSpace(r.SourceDirectory) == "" || strings.TrimSpace(r.SnapshotDirectory) == "" ||
			strings.TrimSpace(r.WorkingDirectory) == "" || strings.TrimSpace(r.Method) == "" || r.FinishedAt != nil || len(r.Result) > 0 {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "等待手动执行的 Mod 分析状态不完整")
		}
	case AuditModAnalysisCompleted:
		if r.FinishedAt == nil || r.FinishedAt.IsZero() || len(r.Result) == 0 || r.Failure != "" {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "已完成 Mod 分析缺少结果或结束时间")
		}
		canonicalResult, resultDigest, resultErr := canonicalJSONObject(r.Result)
		if resultErr != nil || string(canonicalResult) != string(r.Result) || resultDigest != r.ResultSHA256 {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 分析结果或哈希无效", resultErr)
		}
	case AuditModAnalysisFailed:
		if r.FinishedAt == nil || r.FinishedAt.IsZero() || strings.TrimSpace(r.Failure) == "" || len(r.Result) > 0 {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "失败 Mod 分析缺少错误或结束时间")
		}
	}
	if r.Status != AuditModAnalysisCompleted && r.ResultSHA256 != "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "未完成 Mod 分析不能包含结果哈希")
	}
	return nil
}
