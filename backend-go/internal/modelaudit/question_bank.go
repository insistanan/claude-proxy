package modelaudit

import (
	"bufio"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	questionBankManifestFile  = "manifest.json"
	questionBankQuestionsFile = "questions.jsonl"
)

//go:embed examples/question-banks/builtin-core/manifest.json examples/question-banks/builtin-core/questions.jsonl
var builtinQuestionBankFiles embed.FS

type QuestionBankManifest struct {
	ID               string                          `json:"id"`
	Version          string                          `json:"version"`
	Name             string                          `json:"name"`
	Description      string                          `json:"description"`
	DimensionWeights map[CapabilityDimension]float64 `json:"dimensionWeights"`
}

type QuestionBankQuestion struct {
	ID                  string               `json:"id"`
	Dimension           CapabilityDimension  `json:"dimension"`
	Difficulty          string               `json:"difficulty"`
	Prompt              string               `json:"prompt"`
	ExpectedOutput      json.RawMessage      `json:"expectedOutput"`
	ScorerKind          CapabilityScorerKind `json:"scorerKind"`
	ScorerConfig        json.RawMessage      `json:"scorerConfig"`
	Tools               []ToolDefinition     `json:"tools,omitempty"`
	MaximumInputTokens  int                  `json:"maximumInputTokens,omitempty"`
	MaximumOutputTokens int                  `json:"maximumOutputTokens,omitempty"`
	TimeoutMillis       int64                `json:"timeoutMs,omitempty"`
	Weight              float64              `json:"weight,omitempty"`
	DisclosureRisk      DisclosureRisk       `json:"disclosureRisk,omitempty"`
}

type QuestionBankDescriptor struct {
	ID            string    `json:"id"`
	Version       string    `json:"version"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	ContentSHA256 string    `json:"contentSha256"`
	QuestionCount int       `json:"questionCount"`
	SourcePath    string    `json:"sourcePath"`
	SnapshotPath  string    `json:"snapshotPath"`
	LoadedAt      time.Time `json:"loadedAt"`
}

type QuestionBankLoadIssue struct {
	Directory string `json:"directory"`
	BankID    string `json:"bankId,omitempty"`
	Message   string `json:"message"`
}

type QuestionBankCatalogSnapshot struct {
	Banks    []QuestionBankDescriptor `json:"banks"`
	Issues   []QuestionBankLoadIssue  `json:"issues"`
	LoadedAt time.Time                `json:"loadedAt"`
}

type loadedQuestionBank struct {
	descriptor QuestionBankDescriptor
	manifest   QuestionBankManifest
	questions  []QuestionBankQuestion
}

func loadQuestionBanks(sourceRoot, cacheRoot string) ([]loadedQuestionBank, QuestionBankCatalogSnapshot, error) {
	if strings.TrimSpace(sourceRoot) == "" || strings.TrimSpace(cacheRoot) == "" {
		return nil, QuestionBankCatalogSnapshot{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "能力题库目录不能为空")
	}
	if err := os.MkdirAll(sourceRoot, 0755); err != nil {
		return nil, QuestionBankCatalogSnapshot{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "创建能力题库目录失败", err)
	}
	if err := os.MkdirAll(cacheRoot, 0755); err != nil {
		return nil, QuestionBankCatalogSnapshot{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "创建能力题库缓存目录失败", err)
	}
	if err := installBuiltinQuestionBank(sourceRoot); err != nil {
		return nil, QuestionBankCatalogSnapshot{}, err
	}
	entries, err := os.ReadDir(sourceRoot)
	if err != nil {
		return nil, QuestionBankCatalogSnapshot{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "扫描能力题库目录失败", err)
	}
	now := time.Now().UTC()
	banks := make([]loadedQuestionBank, 0, len(entries))
	issues := make([]QuestionBankLoadIssue, 0)
	seenCurrent := make(map[string]struct{})
	seenSnapshots := make(map[string]struct{})
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		directory := filepath.Join(sourceRoot, entry.Name())
		bank, loadErr := loadQuestionBank(directory, cacheRoot, now)
		if loadErr != nil {
			issues = append(issues, QuestionBankLoadIssue{Directory: directory, Message: loadErr.Error()})
			continue
		}
		currentKey := bank.manifest.ID + "@" + bank.manifest.Version
		if _, duplicate := seenCurrent[currentKey]; duplicate {
			issues = append(issues, QuestionBankLoadIssue{Directory: directory, BankID: bank.manifest.ID, Message: "题库 ID 与版本重复"})
			continue
		}
		seenCurrent[currentKey] = struct{}{}
		seenSnapshots[currentKey+"@"+bank.descriptor.ContentSHA256] = struct{}{}
		banks = append(banks, bank)
	}
	currentCount := len(banks)
	historical, historyIssues := loadHistoricalQuestionBanks(cacheRoot, seenSnapshots, now)
	banks = append(banks, historical...)
	issues = append(issues, historyIssues...)
	sort.Slice(banks, func(i, j int) bool { return banks[i].manifest.ID < banks[j].manifest.ID })
	descriptors := make([]QuestionBankDescriptor, 0, currentCount)
	for _, bank := range banks {
		if !pathWithinQuestionBankCache(bank.descriptor.SourcePath, cacheRoot) {
			descriptors = append(descriptors, bank.descriptor)
		}
	}
	sort.Slice(descriptors, func(i, j int) bool { return descriptors[i].ID < descriptors[j].ID })
	return banks, QuestionBankCatalogSnapshot{Banks: descriptors, Issues: issues, LoadedAt: now}, nil
}

func loadHistoricalQuestionBanks(cacheRoot string, seen map[string]struct{}, loadedAt time.Time) ([]loadedQuestionBank, []QuestionBankLoadIssue) {
	banks := make([]loadedQuestionBank, 0)
	issues := make([]QuestionBankLoadIssue, 0)
	_ = filepath.WalkDir(cacheRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			issues = append(issues, QuestionBankLoadIssue{Directory: path, Message: walkErr.Error()})
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if _, err := os.Stat(filepath.Join(path, questionBankManifestFile)); err != nil {
			return nil
		}
		if _, err := os.Stat(filepath.Join(path, questionBankQuestionsFile)); err != nil {
			return nil
		}
		bank, err := loadQuestionBank(path, cacheRoot, loadedAt)
		if err != nil {
			issues = append(issues, QuestionBankLoadIssue{Directory: path, Message: err.Error()})
			return filepath.SkipDir
		}
		key := bank.manifest.ID + "@" + bank.manifest.Version + "@" + bank.descriptor.ContentSHA256
		if _, duplicate := seen[key]; !duplicate {
			seen[key] = struct{}{}
			banks = append(banks, bank)
		}
		return filepath.SkipDir
	})
	return banks, issues
}

func pathWithinQuestionBankCache(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func installBuiltinQuestionBank(root string) error {
	destination := filepath.Join(root, "builtin-core")
	if _, err := os.Stat(destination); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "检查内置题库目录失败", err)
	}
	if err := os.MkdirAll(destination, 0755); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "创建内置题库目录失败", err)
	}
	for _, name := range []string{questionBankManifestFile, questionBankQuestionsFile} {
		content, err := builtinQuestionBankFiles.ReadFile("examples/question-banks/builtin-core/" + name)
		if err != nil {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取内置题库资产失败", err)
		}
		if err := os.WriteFile(filepath.Join(destination, name), content, 0644); err != nil {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "安装内置题库资产失败", err)
		}
	}
	return nil
}

func loadQuestionBank(directory, cacheRoot string, loadedAt time.Time) (loadedQuestionBank, error) {
	manifestBytes, err := os.ReadFile(filepath.Join(directory, questionBankManifestFile))
	if err != nil {
		return loadedQuestionBank{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "题库缺少 manifest.json", err)
	}
	var manifest QuestionBankManifest
	if err := decodeStrictJSON(manifestBytes, &manifest); err != nil {
		return loadedQuestionBank{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "题库 manifest.json 无效", err)
	}
	if !stableIDPattern.MatchString(manifest.ID) || !semanticVersionPattern.MatchString(manifest.Version) || strings.TrimSpace(manifest.Name) == "" || strings.TrimSpace(manifest.Description) == "" {
		return loadedQuestionBank{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "题库 ID、版本、名称或说明无效")
	}
	questionBytes, err := os.ReadFile(filepath.Join(directory, questionBankQuestionsFile))
	if err != nil {
		return loadedQuestionBank{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "题库缺少 questions.jsonl", err)
	}
	questions, err := decodeQuestionBankQuestions(questionBytes)
	if err != nil {
		return loadedQuestionBank{}, err
	}
	hash := sha256.New()
	_, _ = hash.Write(manifestBytes)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(questionBytes)
	digest := hex.EncodeToString(hash.Sum(nil))
	snapshotPath := filepath.Join(cacheRoot, manifest.ID, manifest.Version, digest)
	if err := writeQuestionBankSnapshot(snapshotPath, manifestBytes, questionBytes); err != nil {
		return loadedQuestionBank{}, err
	}
	return loadedQuestionBank{
		descriptor: QuestionBankDescriptor{ID: manifest.ID, Version: manifest.Version, Name: manifest.Name, Description: manifest.Description, ContentSHA256: digest, QuestionCount: len(questions), SourcePath: directory, SnapshotPath: snapshotPath, LoadedAt: loadedAt},
		manifest:   manifest, questions: questions,
	}, nil
}

func decodeQuestionBankQuestions(content []byte) ([]QuestionBankQuestion, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	questions := make([]QuestionBankQuestion, 0)
	seen := make(map[string]struct{})
	for line := 1; scanner.Scan(); line++ {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		var question QuestionBankQuestion
		if err := decodeStrictJSON([]byte(raw), &question); err != nil {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "questions.jsonl 第 "+strconvItoa(line)+" 行无效", err)
		}
		if err := validateQuestionBankQuestion(question); err != nil {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "questions.jsonl 第 "+strconvItoa(line)+" 行无效", err)
		}
		if _, duplicate := seen[question.ID]; duplicate {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "题目 ID 重复: "+question.ID)
		}
		seen[question.ID] = struct{}{}
		questions = append(questions, question)
	}
	if err := scanner.Err(); err != nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "读取 questions.jsonl 失败", err)
	}
	if len(questions) < len(capabilityDimensions) {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "题库至少需要七道题并覆盖全部能力维度")
	}
	covered := make(map[CapabilityDimension]bool)
	for _, question := range questions {
		covered[question.Dimension] = true
	}
	for _, dimension := range capabilityDimensions {
		if !covered[dimension] {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "题库未覆盖能力维度 "+string(dimension))
		}
	}
	return questions, nil
}

func validateQuestionBankQuestion(q QuestionBankQuestion) error {
	if !stableIDPattern.MatchString(q.ID) || !q.Dimension.Valid() || !stableIDPattern.MatchString(q.Difficulty) || strings.TrimSpace(q.Prompt) == "" || !q.ScorerKind.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "题目 ID、维度、难度、输入或判分器无效")
	}
	if len(q.ExpectedOutput) == 0 || !json.Valid(q.ExpectedOutput) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "题目 expectedOutput 必须是合法 JSON")
	}
	if len(q.ScorerConfig) == 0 {
		q.ScorerConfig = json.RawMessage(`{}`)
	}
	scorer, err := NewBuiltinCapabilityScorer(q.ScorerKind)
	if err != nil {
		return err
	}
	return scorer.ValidateConfig(q.ScorerConfig)
}

func decodeStrictJSON(content []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "JSON 只能包含一个对象", err)
	}
	return nil
}

func writeQuestionBankSnapshot(directory string, manifest, questions []byte) error {
	if _, err := os.Stat(filepath.Join(directory, questionBankManifestFile)); err == nil {
		return nil
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "创建题库快照目录失败", err)
	}
	if err := os.WriteFile(filepath.Join(directory, questionBankManifestFile), manifest, 0444); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "写入题库 manifest 快照失败", err)
	}
	if err := os.WriteFile(filepath.Join(directory, questionBankQuestionsFile), questions, 0444); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "写入题库题目快照失败", err)
	}
	return nil
}

func strconvItoa(value int) string {
	return strconv.Itoa(value)
}
