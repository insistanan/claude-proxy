package modelaudit

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const auditModManifestFile = "manifest.json"

type AuditModCatalog struct {
	sourceRoot string
	cacheRoot  string
	now        func() time.Time

	mu       sync.RWMutex
	byID     map[string]AuditModBundle
	issues   []AuditModLoadIssue
	loadedAt time.Time
}

func NewAuditModCatalog(sourceRoot, cacheRoot string) (*AuditModCatalog, error) {
	sourceRoot = strings.TrimSpace(sourceRoot)
	cacheRoot = strings.TrimSpace(cacheRoot)
	if sourceRoot == "" || cacheRoot == "" {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 源目录和缓存目录不能为空")
	}
	absSource, err := filepath.Abs(sourceRoot)
	if err != nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "解析 Mod 源目录失败", err)
	}
	absCache, err := filepath.Abs(cacheRoot)
	if err != nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "解析 Mod 缓存目录失败", err)
	}
	if sameAuditModPath(absSource, absCache) || pathWithinAuditModRoot(absSource, absCache) || pathWithinAuditModRoot(absCache, absSource) {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 源目录和缓存目录不能相同或互相嵌套")
	}
	for _, directory := range []string{absSource, absCache} {
		if err := os.MkdirAll(directory, 0755); err != nil {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "创建 Mod 目录失败", err)
		}
	}
	return &AuditModCatalog{
		sourceRoot: absSource,
		cacheRoot:  absCache,
		now:        time.Now,
		byID:       make(map[string]AuditModBundle),
	}, nil
}

func (c *AuditModCatalog) Reload(ctx context.Context) (AuditModCatalogSnapshot, error) {
	if c == nil {
		return AuditModCatalogSnapshot{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "Mod 目录未初始化")
	}
	entries, err := os.ReadDir(c.sourceRoot)
	if err != nil {
		return AuditModCatalogSnapshot{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取 Mod 根目录失败", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	loadedAt := c.now().UTC()
	loaded := make(map[string]AuditModBundle)
	issues := make([]AuditModLoadIssue, 0)
	duplicateIDs := make(map[string]struct{})
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return AuditModCatalogSnapshot{}, err
		}
		if !entry.IsDir() {
			continue
		}
		directory := filepath.Join(c.sourceRoot, entry.Name())
		bundle, loadErr := loadAuditModBundle(directory, loadedAt)
		if loadErr != nil {
			issues = append(issues, AuditModLoadIssue{Directory: directory, Message: loadErr.Error()})
			continue
		}
		if _, duplicate := loaded[bundle.Manifest.ID]; duplicate {
			duplicateIDs[bundle.Manifest.ID] = struct{}{}
			issues = append(issues, AuditModLoadIssue{
				Directory: directory,
				ModID:     bundle.Manifest.ID,
				Message:   fmt.Sprintf("Mod ID %q 重复，冲突目录均未加载", bundle.Manifest.ID),
			})
			continue
		}
		if _, duplicate := duplicateIDs[bundle.Manifest.ID]; duplicate {
			issues = append(issues, AuditModLoadIssue{
				Directory: directory,
				ModID:     bundle.Manifest.ID,
				Message:   fmt.Sprintf("Mod ID %q 重复，冲突目录均未加载", bundle.Manifest.ID),
			})
			continue
		}
		loaded[bundle.Manifest.ID] = bundle
	}
	for id := range duplicateIDs {
		if first, exists := loaded[id]; exists {
			issues = append(issues, AuditModLoadIssue{
				Directory: first.SourcePath,
				ModID:     id,
				Message:   fmt.Sprintf("Mod ID %q 重复，冲突目录均未加载", id),
			})
			delete(loaded, id)
		}
	}
	for id, bundle := range loaded {
		snapshotPath, snapshotErr := c.ensureSnapshot(bundle)
		if snapshotErr != nil {
			issues = append(issues, AuditModLoadIssue{Directory: bundle.SourcePath, ModID: id, Message: snapshotErr.Error()})
			delete(loaded, id)
			continue
		}
		bundle.SnapshotPath = snapshotPath
		loaded[id] = bundle
	}
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Directory == issues[j].Directory {
			return issues[i].Message < issues[j].Message
		}
		return issues[i].Directory < issues[j].Directory
	})

	c.mu.Lock()
	c.byID = loaded
	c.issues = append([]AuditModLoadIssue(nil), issues...)
	c.loadedAt = loadedAt
	snapshot := c.snapshotLocked()
	c.mu.Unlock()
	return snapshot, nil
}

func (c *AuditModCatalog) Snapshot() AuditModCatalogSnapshot {
	if c == nil {
		return emptyAuditModCatalogSnapshot()
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshotLocked()
}

func (c *AuditModCatalog) Get(reference AuditModReference) (AuditModBundle, bool) {
	if c == nil || reference.Validate() != nil {
		return AuditModBundle{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	bundle, found := c.byID[reference.ID]
	if !found || bundle.Reference != reference {
		return AuditModBundle{}, false
	}
	return cloneAuditModBundle(bundle), true
}

func (c *AuditModCatalog) GetCurrent(id string) (AuditModBundle, bool) {
	if c == nil || !stableIDPattern.MatchString(id) {
		return AuditModBundle{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	bundle, found := c.byID[id]
	if !found {
		return AuditModBundle{}, false
	}
	return cloneAuditModBundle(bundle), true
}

func (c *AuditModCatalog) snapshotLocked() AuditModCatalogSnapshot {
	mods := make([]AuditModDescriptor, 0, len(c.byID))
	for _, bundle := range c.byID {
		mods = append(mods, AuditModDescriptor{
			Reference: bundle.Reference, Name: bundle.Manifest.Name, Description: bundle.Manifest.Description,
			AnalysisMode: bundle.Manifest.Analysis.Mode, SourcePath: bundle.SourcePath,
			SnapshotPath: bundle.SnapshotPath, LoadedAt: bundle.LoadedAt,
		})
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Reference.ID < mods[j].Reference.ID })
	issues := make([]AuditModLoadIssue, len(c.issues))
	copy(issues, c.issues)
	return AuditModCatalogSnapshot{Mods: mods, Issues: issues, LoadedAt: c.loadedAt}
}

func emptyAuditModCatalogSnapshot() AuditModCatalogSnapshot {
	return AuditModCatalogSnapshot{
		Mods:   make([]AuditModDescriptor, 0),
		Issues: make([]AuditModLoadIssue, 0),
	}
}

func loadAuditModBundle(directory string, loadedAt time.Time) (AuditModBundle, error) {
	files := make(map[string][]byte)
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == directory {
			return nil
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("Mod 包含不支持的非普通文件 %q", relative)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[relative] = content
		return nil
	})
	if err != nil {
		return AuditModBundle{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "读取 Mod 目录失败", err)
	}
	manifestContent, found := files[auditModManifestFile]
	if !found {
		return AuditModBundle{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 目录缺少 manifest.json")
	}
	manifest, err := decodeAuditModManifest(manifestContent)
	if err != nil {
		return AuditModBundle{}, err
	}
	if err := validateAuditModReferencedFiles(manifest, files); err != nil {
		return AuditModBundle{}, err
	}
	digest := auditModBundleSHA256(files)
	return AuditModBundle{
		Manifest:   manifest,
		Reference:  AuditModReference{ID: manifest.ID, Version: manifest.Version, ContentSHA256: digest},
		SourcePath: directory,
		Files:      files,
		LoadedAt:   loadedAt,
	}, nil
}

func validateAuditModReferencedFiles(manifest AuditModManifest, files map[string][]byte) error {
	for _, path := range manifest.referencedFiles() {
		content, found := files[path]
		if !found {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("Mod 引用文件 %q 不存在", path))
		}
		if len(strings.TrimSpace(string(content))) == 0 {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("Mod 引用文件 %q 为空", path))
		}
	}
	for _, path := range []string{manifest.RulesFile, manifest.Analysis.ResultSchemaFile} {
		normalized, err := normalizeAuditModRelativePath(path, "Mod JSON 文件")
		if err != nil {
			return err
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(files[normalized], &object); err != nil || object == nil {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("Mod JSON 文件 %q 必须是对象", normalized), err)
		}
	}
	return nil
}

func auditModBundleSHA256(files map[string][]byte) string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, filepath.ToSlash(path))
	}
	sort.Strings(paths)
	hash := sha256.New()
	var length [8]byte
	for _, path := range paths {
		content := files[path]
		binary.BigEndian.PutUint64(length[:], uint64(len(path)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(path))
		binary.BigEndian.PutUint64(length[:], uint64(len(content)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(content)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (c *AuditModCatalog) ensureSnapshot(bundle AuditModBundle) (string, error) {
	destination := filepath.Join(c.cacheRoot, bundle.Reference.ContentSHA256)
	if err := validateAuditModSnapshot(destination, bundle.Reference); err == nil {
		return destination, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	temporary, err := os.MkdirTemp(c.cacheRoot, ".snapshot-")
	if err != nil {
		return "", contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "创建 Mod 临时快照目录失败", err)
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	paths := make([]string, 0, len(bundle.Files))
	for path := range bundle.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, relative := range paths {
		target := filepath.Join(temporary, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return "", contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "创建 Mod 快照子目录失败", err)
		}
		if err := os.WriteFile(target, bundle.Files[relative], 0644); err != nil {
			return "", contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "写入 Mod 快照失败", err)
		}
	}
	marker, err := json.Marshal(bundle.Reference)
	if err != nil {
		return "", contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码 Mod 快照标记失败", err)
	}
	if err := os.WriteFile(filepath.Join(temporary, ".snapshot.json"), marker, 0644); err != nil {
		return "", contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "写入 Mod 快照标记失败", err)
	}
	if err := os.Rename(temporary, destination); err != nil {
		if validationErr := validateAuditModSnapshot(destination, bundle.Reference); validationErr == nil {
			return destination, nil
		}
		return "", contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "提交 Mod 内容快照失败", err)
	}
	return destination, nil
}

func validateAuditModSnapshot(directory string, expected AuditModReference) error {
	content, err := os.ReadFile(filepath.Join(directory, ".snapshot.json"))
	if err != nil {
		return err
	}
	var actual AuditModReference
	if err := json.Unmarshal(content, &actual); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "Mod 缓存快照标记损坏", err)
	}
	if actual != expected {
		return contractError(ErrorCodeConflict, ErrorCategoryInternal, "Mod 缓存快照与内容 SHA 不一致")
	}
	return nil
}

func cloneAuditModBundle(bundle AuditModBundle) AuditModBundle {
	clone := bundle
	clone.Manifest.Parser.Choices = append([]string(nil), bundle.Manifest.Parser.Choices...)
	clone.Manifest.Aggregation.Buckets = append([]AuditModHistogramBucket(nil), bundle.Manifest.Aggregation.Buckets...)
	if bundle.Manifest.Analysis.Python != nil {
		python := *bundle.Manifest.Analysis.Python
		python.Arguments = append([]string(nil), bundle.Manifest.Analysis.Python.Arguments...)
		clone.Manifest.Analysis.Python = &python
	}
	if bundle.Manifest.Analysis.Manual != nil {
		manual := *bundle.Manifest.Analysis.Manual
		clone.Manifest.Analysis.Manual = &manual
	}
	clone.Files = make(map[string][]byte, len(bundle.Files))
	for path, content := range bundle.Files {
		clone.Files[path] = append([]byte(nil), content...)
	}
	return clone
}

func sameAuditModPath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func pathWithinAuditModRoot(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
