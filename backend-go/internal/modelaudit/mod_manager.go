package modelaudit

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

var auditModDirectoryPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

type AuditModEditable struct {
	Descriptor  AuditModDescriptor `json:"descriptor"`
	Manifest    AuditModManifest   `json:"manifest"`
	Files       map[string]string  `json:"files"`
	BinaryFiles []string           `json:"binaryFiles,omitempty"`
}

type AuditModWriteRequest struct {
	DirectoryName         string            `json:"directoryName"`
	ExpectedContentSHA256 string            `json:"expectedContentSha256,omitempty"`
	Files                 map[string]string `json:"files"`
}

type AuditModImportRequest struct {
	SourcePath            string `json:"sourcePath"`
	DirectoryName         string `json:"directoryName,omitempty"`
	ExpectedContentSHA256 string `json:"expectedContentSha256,omitempty"`
}

type AuditModManager struct {
	catalog    *AuditModCatalog
	store      *AuditSQLiteStore
	strategies *StrategyRegistry

	mu       sync.RWMutex
	writeMu  sync.Mutex
	snapshot AuditModCatalogSnapshot
}

func NewAuditModManager(catalog *AuditModCatalog, store *AuditSQLiteStore, strategies *StrategyRegistry) (*AuditModManager, error) {
	if catalog == nil || store == nil || strategies == nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "Mod 管理器依赖未初始化")
	}
	return &AuditModManager{catalog: catalog, store: store, strategies: strategies}, nil
}

func (m *AuditModManager) Reload(ctx context.Context) (AuditModCatalogSnapshot, error) {
	if m == nil {
		return AuditModCatalogSnapshot{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "Mod 管理器未初始化")
	}
	fileSnapshot, err := m.catalog.Reload(ctx)
	if err != nil {
		return AuditModCatalogSnapshot{}, err
	}
	strategies := make([]Strategy, 0, len(fileSnapshot.Mods))
	mods := make([]AuditModDescriptor, 0, len(fileSnapshot.Mods))
	issues := make([]AuditModLoadIssue, len(fileSnapshot.Issues))
	copy(issues, fileSnapshot.Issues)
	for _, descriptor := range fileSnapshot.Mods {
		if m.strategies.IsStatic(descriptor.Reference.ID) {
			issues = append(issues, AuditModLoadIssue{
				Directory: descriptor.SourcePath, ModID: descriptor.Reference.ID,
				Message: fmt.Sprintf("Mod ID %q 与内置策略冲突", descriptor.Reference.ID),
			})
			continue
		}
		bundle, found := m.catalog.Get(descriptor.Reference)
		if !found {
			issues = append(issues, AuditModLoadIssue{Directory: descriptor.SourcePath, ModID: descriptor.Reference.ID, Message: "Mod 内容快照在加载期间发生变化"})
			continue
		}
		strategy, strategyErr := NewDeclarativeAuditModStrategy(bundle)
		if strategyErr != nil {
			issues = append(issues, AuditModLoadIssue{Directory: descriptor.SourcePath, ModID: descriptor.Reference.ID, Message: strategyErr.Error()})
			continue
		}
		if _, storeErr := m.store.SaveModVersion(ctx, bundle); storeErr != nil {
			issues = append(issues, AuditModLoadIssue{Directory: descriptor.SourcePath, ModID: descriptor.Reference.ID, Message: storeErr.Error()})
			continue
		}
		strategies = append(strategies, strategy)
		mods = append(mods, descriptor)
	}
	if err := m.strategies.ReplaceDynamic(strategies...); err != nil {
		return AuditModCatalogSnapshot{}, err
	}
	historicalVersions, err := m.store.ListModVersions(ctx)
	if err != nil {
		return AuditModCatalogSnapshot{}, err
	}
	for _, version := range historicalVersions {
		bundle, bundleErr := loadAuditModStoredBundle(version)
		if bundleErr != nil {
			issues = append(issues, AuditModLoadIssue{Directory: version.SnapshotPath, ModID: version.Reference.ID, Message: bundleErr.Error()})
			continue
		}
		strategy, strategyErr := NewDeclarativeAuditModStrategy(bundle)
		if strategyErr == nil {
			strategyErr = m.strategies.RegisterDynamicVersion(strategy)
		}
		if strategyErr != nil {
			issues = append(issues, AuditModLoadIssue{Directory: version.SnapshotPath, ModID: version.Reference.ID, Message: strategyErr.Error()})
		}
	}
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Directory == issues[j].Directory {
			return issues[i].Message < issues[j].Message
		}
		return issues[i].Directory < issues[j].Directory
	})
	snapshot := AuditModCatalogSnapshot{Mods: mods, Issues: issues, LoadedAt: fileSnapshot.LoadedAt}
	m.mu.Lock()
	m.snapshot = cloneAuditModCatalogSnapshot(snapshot)
	m.mu.Unlock()
	return snapshot, nil
}

func loadAuditModStoredBundle(version AuditModStoredVersion) (AuditModBundle, error) {
	bundle, err := loadAuditModBundle(version.SnapshotPath, version.LoadedAt)
	if err != nil {
		return AuditModBundle{}, err
	}
	delete(bundle.Files, ".snapshot.json")
	bundle.Reference = AuditModReference{
		ID: bundle.Manifest.ID, Version: bundle.Manifest.Version, ContentSHA256: auditModBundleSHA256(bundle.Files),
	}
	if bundle.Reference != version.Reference {
		return AuditModBundle{}, contractError(ErrorCodeConflict, ErrorCategoryInternal, "Mod 历史快照内容与数据库版本不一致")
	}
	bundle.SourcePath = version.SourcePath
	bundle.SnapshotPath = version.SnapshotPath
	return bundle, nil
}

func (m *AuditModManager) Snapshot() AuditModCatalogSnapshot {
	if m == nil {
		return emptyAuditModCatalogSnapshot()
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneAuditModCatalogSnapshot(m.snapshot)
}

func (m *AuditModManager) Get(reference AuditModReference) (AuditModBundle, bool) {
	if m == nil {
		return AuditModBundle{}, false
	}
	return m.catalog.Get(reference)
}

func (m *AuditModManager) GetStored(ctx context.Context, reference AuditModReference) (AuditModBundle, bool, error) {
	if bundle, found := m.Get(reference); found {
		return bundle, true, nil
	}
	version, found, err := m.store.GetModVersion(ctx, reference)
	if err != nil || !found {
		return AuditModBundle{}, found, err
	}
	bundle, err := loadAuditModStoredBundle(version)
	if err != nil {
		return AuditModBundle{}, false, err
	}
	return bundle, true, nil
}

func (m *AuditModManager) Editable(id string) (AuditModEditable, error) {
	if m == nil || m.catalog == nil {
		return AuditModEditable{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "Mod 管理器未初始化")
	}
	bundle, found := m.catalog.GetCurrent(strings.TrimSpace(id))
	if !found {
		return AuditModEditable{}, auditNotFound("Mod", id)
	}
	files := make(map[string]string, len(bundle.Files))
	binaryFiles := make([]string, 0)
	for path, content := range bundle.Files {
		if isAuditModTextFile(path, content) {
			files[path] = string(content)
		} else {
			binaryFiles = append(binaryFiles, path)
		}
	}
	sort.Strings(binaryFiles)
	return AuditModEditable{
		Descriptor: AuditModDescriptor{
			Reference: bundle.Reference, Name: bundle.Manifest.Name, Description: bundle.Manifest.Description,
			AnalysisMode: bundle.Manifest.Analysis.Mode, SourcePath: bundle.SourcePath,
			SnapshotPath: bundle.SnapshotPath, LoadedAt: bundle.LoadedAt,
		},
		Manifest: bundle.Manifest, Files: files, BinaryFiles: binaryFiles,
	}, nil
}

func (m *AuditModManager) Save(ctx context.Context, requestedID string, request AuditModWriteRequest) (AuditModEditable, error) {
	files := make(map[string][]byte, len(request.Files))
	for path, content := range request.Files {
		normalized, err := normalizeAuditModRelativePath(path, "Mod 文件")
		if err != nil {
			return AuditModEditable{}, err
		}
		if _, duplicate := files[normalized]; duplicate {
			return AuditModEditable{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 文件路径规范化后重复")
		}
		files[normalized] = []byte(content)
	}
	if expected := strings.TrimSpace(request.ExpectedContentSHA256); expected != "" {
		current, found := m.catalog.GetCurrent(strings.TrimSpace(requestedID))
		if !found || current.Reference.ContentSHA256 != expected {
			return AuditModEditable{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "Mod 已被修改，请刷新后重试")
		}
		for path, content := range current.Files {
			if _, supplied := files[path]; !supplied && !isAuditModTextFile(path, content) {
				files[path] = append([]byte(nil), content...)
			}
		}
	}
	bundle, err := m.write(ctx, requestedID, request.DirectoryName, request.ExpectedContentSHA256, files)
	if err != nil {
		return AuditModEditable{}, err
	}
	return m.Editable(bundle.Reference.ID)
}

func (m *AuditModManager) Import(ctx context.Context, request AuditModImportRequest) (AuditModEditable, error) {
	source, err := filepath.Abs(strings.TrimSpace(request.SourcePath))
	if err != nil || strings.TrimSpace(request.SourcePath) == "" {
		return AuditModEditable{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "待导入 Mod 目录无效", err)
	}
	bundle, err := loadAuditModBundle(source, m.catalog.now().UTC())
	if err != nil {
		return AuditModEditable{}, err
	}
	directoryName := strings.TrimSpace(request.DirectoryName)
	if directoryName == "" {
		directoryName = filepath.Base(source)
	}
	written, err := m.write(ctx, bundle.Manifest.ID, directoryName, request.ExpectedContentSHA256, bundle.Files)
	if err != nil {
		return AuditModEditable{}, err
	}
	return m.Editable(written.Reference.ID)
}

func (m *AuditModManager) write(
	ctx context.Context,
	requestedID string,
	directoryName string,
	expectedSHA string,
	files map[string][]byte,
) (AuditModBundle, error) {
	if m == nil || m.catalog == nil {
		return AuditModBundle{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "Mod 管理器未初始化")
	}
	manifestContent, found := files[auditModManifestFile]
	if !found {
		return AuditModBundle{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 文件缺少 manifest.json")
	}
	manifest, err := decodeAuditModManifest(manifestContent)
	if err != nil {
		return AuditModBundle{}, err
	}
	directoryName = strings.TrimSpace(directoryName)
	if requestedID = strings.TrimSpace(requestedID); requestedID != "" {
		if current, found := m.catalog.GetCurrent(requestedID); found {
			currentDirectory := filepath.Base(current.SourcePath)
			if directoryName == "" {
				directoryName = currentDirectory
			} else if directoryName != currentDirectory {
				return AuditModBundle{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "更新 Mod 时不能修改源目录名")
			}
		}
	}
	if directoryName == "" {
		directoryName = manifest.ID
	}
	if !auditModDirectoryPattern.MatchString(directoryName) || directoryName == "." || directoryName == ".." {
		return AuditModBundle{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 目录名只能包含小写字母、数字、点、下划线和连字符")
	}
	if requestedID != "" && manifest.ID != requestedID {
		return AuditModBundle{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "请求路径中的 Mod ID 与 manifest.json 不一致")
	}

	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	target := filepath.Join(m.catalog.sourceRoot, directoryName)
	if !pathWithinAuditModRoot(target, m.catalog.sourceRoot) {
		return AuditModBundle{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 目标目录越界")
	}
	_, statErr := os.Stat(target)
	targetExists := statErr == nil
	if statErr != nil && !errorsIsNotExist(statErr) {
		return AuditModBundle{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取 Mod 目标目录失败", statErr)
	}
	expectedSHA = strings.TrimSpace(expectedSHA)
	if targetExists {
		if expectedSHA == "" {
			return AuditModBundle{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "Mod 目录已存在")
		}
		current, err := loadAuditModBundle(target, m.catalog.now().UTC())
		if err != nil || current.Reference.ContentSHA256 != expectedSHA {
			return AuditModBundle{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "Mod 目录内容已经变化，请重载后重试", err)
		}
	} else if expectedSHA != "" {
		return AuditModBundle{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "待更新的 Mod 目录不存在")
	}

	temporary, err := os.MkdirTemp(m.catalog.sourceRoot, ".mod-write-")
	if err != nil {
		return AuditModBundle{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "创建 Mod 写入临时目录失败", err)
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	for path, content := range files {
		normalized, err := normalizeAuditModRelativePath(path, "Mod 文件")
		if err != nil {
			return AuditModBundle{}, err
		}
		destination := filepath.Join(temporary, filepath.FromSlash(normalized))
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return AuditModBundle{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "创建 Mod 子目录失败", err)
		}
		if err := os.WriteFile(destination, content, 0644); err != nil {
			return AuditModBundle{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "写入 Mod 文件失败", err)
		}
	}
	validated, err := loadAuditModBundle(temporary, m.catalog.now().UTC())
	if err != nil {
		return AuditModBundle{}, err
	}
	if validated.Manifest.ID != manifest.ID {
		return AuditModBundle{}, contractError(ErrorCodeConflict, ErrorCategoryInternal, "Mod 临时校验结果不一致")
	}

	backup := target + ".backup"
	if _, err := os.Stat(backup); err == nil {
		return AuditModBundle{}, contractError(ErrorCodeConflict, ErrorCategoryInternal, "Mod 备份目录已存在，请先人工检查")
	}
	if targetExists {
		if err := os.Rename(target, backup); err != nil {
			return AuditModBundle{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "备份现有 Mod 目录失败", err)
		}
	}
	committed := false
	defer func() {
		if committed || !targetExists {
			return
		}
		if os.Rename(backup, target) == nil {
			_, _ = m.Reload(context.Background())
		}
	}()
	if err := os.Rename(temporary, target); err != nil {
		return AuditModBundle{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "提交 Mod 目录失败", err)
	}
	snapshot, err := m.Reload(ctx)
	if err != nil {
		_ = os.RemoveAll(target)
		if !targetExists {
			_, _ = m.Reload(context.Background())
		}
		return AuditModBundle{}, err
	}
	loaded, found := m.catalog.GetCurrent(manifest.ID)
	if !found || !sameAuditModPath(loaded.SourcePath, target) {
		_ = os.RemoveAll(target)
		if !targetExists {
			_, _ = m.Reload(context.Background())
		}
		return AuditModBundle{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 保存后未能通过目录加载校验: "+auditModIssueSummary(snapshot.Issues, target))
	}
	committed = true
	if targetExists {
		_ = os.RemoveAll(backup)
	}
	return loaded, nil
}

func isAuditModTextFile(path string, content []byte) bool {
	extension := strings.ToLower(filepath.Ext(path))
	switch extension {
	case ".json", ".md", ".txt", ".py", ".java", ".js", ".ts", ".sh", ".ps1", ".bat", ".cmd", ".yaml", ".yml":
		return !strings.ContainsRune(string(content), '\x00')
	default:
		return false
	}
}

func auditModIssueSummary(issues []AuditModLoadIssue, directory string) string {
	for _, issue := range issues {
		if sameAuditModPath(issue.Directory, directory) {
			return issue.Message
		}
	}
	return "目录未出现在已加载 Mod 中"
}

func errorsIsNotExist(err error) bool {
	return err != nil && errors.Is(err, fs.ErrNotExist)
}

func cloneAuditModCatalogSnapshot(snapshot AuditModCatalogSnapshot) AuditModCatalogSnapshot {
	mods := make([]AuditModDescriptor, len(snapshot.Mods))
	copy(mods, snapshot.Mods)
	issues := make([]AuditModLoadIssue, len(snapshot.Issues))
	copy(issues, snapshot.Issues)
	return AuditModCatalogSnapshot{
		Mods: mods, Issues: issues,
		LoadedAt: snapshot.LoadedAt,
	}
}
