package handlers

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

const maxSkillImportBytes = 20 << 20

// SkillsAPI 聚合 skills 管理的互斥锁，由 main.go 构造并注入路由，
// 替代包级全局状态；互斥锁串行化对多个 agent 技能目录的读写操作。
type SkillsAPI struct {
	mu sync.Mutex
}

// NewSkillsAPI 创建 skills 管理 API。
func NewSkillsAPI() *SkillsAPI {
	return &SkillsAPI{}
}

var (
	skillNamePattern            = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	skillBackupDirectoryPattern = regexp.MustCompile(`^\d{8}-\d{6}\.\d+$`)
)

type skillLocation struct {
	Key        string `json:"key"`
	Agent      string `json:"agent"`
	Path       string `json:"path"`
	SourceType string `json:"sourceType"`
	ReadOnly   bool   `json:"readOnly"`
	Exists     bool   `json:"exists"`
}

type skillItem struct {
	LocationKey    string `json:"locationKey"`
	Agent          string `json:"agent"`
	Name           string `json:"name"`
	TranslatedName string `json:"translatedName,omitempty"`
	Note           string `json:"note,omitempty"`
	Description    string `json:"description"`
	Path           string `json:"path"`
	Size           int64  `json:"size"`
	ModifiedAt     string `json:"modifiedAt"`
	Valid          bool   `json:"valid"`
	Issue          string `json:"issue,omitempty"`
	SourceType     string `json:"sourceType"`
	ReadOnly       bool   `json:"readOnly"`
}

type skillImportRequest struct {
	FileName      string   `json:"fileName"`
	ContentBase64 string   `json:"contentBase64"`
	Targets       []string `json:"targets"`
}

type skillReferenceRequest struct {
	LocationKey string `json:"locationKey"`
	Name        string `json:"name"`
}

type skillBackupRequest struct {
	LocationKey string `json:"locationKey"`
	Name        string `json:"name"`
	Original    string `json:"original"`
	Translated  string `json:"translated"`
	Model       string `json:"model"`
	ChannelName string `json:"channelName"`
}

type skillBackupMetadata struct {
	Name         string `json:"name"`
	SourcePath   string `json:"sourcePath"`
	SourceSHA256 string `json:"sourceSha256,omitempty"`
	Agent        string `json:"agent"`
	Model        string `json:"model"`
	ChannelName  string `json:"channelName"`
	CreatedAt    string `json:"createdAt"`
}

type skillNoteRequest struct {
	Name string `json:"name"`
	Note string `json:"note"`
}

type skillGlobalMetadata struct {
	Note      string `json:"note"`
	UpdatedAt string `json:"updatedAt"`
}

type skillCopyRequest struct {
	LocationKey string   `json:"locationKey"`
	Name        string   `json:"name"`
	Targets     []string `json:"targets"`
}

// ListSkills 仅枚举当前运行用户的已知 Agent Skill 目录，避免把任意路径暴露为文件浏览器。
func (s *SkillsAPI) List() gin.HandlerFunc {
	return func(c *gin.Context) {
		s.mu.Lock()
		defer s.mu.Unlock()

		locations, err := managedSkillLocations()
		if err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("解析 Skills 目录失败: %v", err)})
			return
		}

		items := make([]skillItem, 0)
		for _, location := range locations {
			if location.Key == "project" {
				if err := restoreProjectBackupSkills(location.Path); err != nil {
					c.JSON(500, gin.H{"error": fmt.Sprintf("整理项目 Skills 备份失败: %v", err)})
					return
				}
			}
			found, err := scanSkillLocation(location)
			if err != nil {
				c.JSON(500, gin.H{"error": fmt.Sprintf("扫描 %s 失败: %v", location.Path, err)})
				return
			}
			items = append(items, found...)
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].Agent == items[j].Agent {
				return items[i].Name < items[j].Name
			}
			return items[i].Agent < items[j].Agent
		})
		c.JSON(200, gin.H{"locations": locations, "skills": items})
	}
}

func (s *SkillsAPI) GetContent() gin.HandlerFunc {
	return func(c *gin.Context) {
		ref, ok := bindSkillReference(c)
		if !ok {
			return
		}
		content, err := os.ReadFile(filepath.Join(ref.Path, "SKILL.md"))
		if err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("读取 Skill 失败: %v", err)})
			return
		}
		c.JSON(200, gin.H{"content": string(content), "path": filepath.Join(ref.Path, "SKILL.md")})
	}
}

// GetLatestSkillBackup 返回与当前内容匹配的最近一份译文。
// 相同原文的不同 Agent 副本共享译文；原文内容变化时不恢复旧译文。
func (s *SkillsAPI) GetLatestBackup() gin.HandlerFunc {
	return func(c *gin.Context) {
		ref, ok := bindSkillReference(c)
		if !ok {
			return
		}
		original, err := os.ReadFile(filepath.Join(ref.Path, "SKILL.md"))
		if err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("读取原 Skill 失败: %v", err)})
			return
		}
		backup, found, err := latestSkillBackup(ref, original)
		if err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("读取翻译备份失败: %v", err)})
			return
		}
		if !found {
			c.JSON(200, gin.H{"found": false})
			return
		}
		c.JSON(200, gin.H{
			"found": true, "translated": backup.Translated, "path": backup.Path,
			"createdAt": backup.Metadata.CreatedAt, "model": backup.Metadata.Model, "channelName": backup.Metadata.ChannelName,
		})
	}
}

// UpdateSkillNote 保存项目级备注。同名 Skill 无论位于哪个 Agent 目录都会读取这份备注。
func (s *SkillsAPI) UpdateNote() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req skillNoteRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "Skill 备注请求无效"})
			return
		}
		if !skillNamePattern.MatchString(req.Name) {
			c.JSON(400, gin.H{"error": "Skill 名称必须为小写字母、数字和连字符"})
			return
		}
		note := strings.TrimSpace(req.Note)
		if len([]rune(note)) > 500 {
			c.JSON(400, gin.H{"error": "备注不能超过 500 个字符"})
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := writeSkillGlobalMetadata(req.Name, skillGlobalMetadata{Note: note, UpdatedAt: time.Now().Format(time.RFC3339)}); err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("保存 Skill 备注失败: %v", err)})
			return
		}
		c.JSON(200, gin.H{"success": true, "note": note})
	}
}

// ConsolidateSkills 将已安装在各 Agent 目录中的有效 Skill 去重后归档到项目目录。
// 已存在的项目副本优先保留，避免其他来源的同名版本覆盖项目中的统一版本。
func (s *SkillsAPI) Consolidate() gin.HandlerFunc {
	return func(c *gin.Context) {
		s.mu.Lock()
		defer s.mu.Unlock()

		locations, err := managedSkillLocations()
		if err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("解析 Skills 目录失败: %v", err)})
			return
		}
		var projectLocation *skillLocation
		for index := range locations {
			if locations[index].Key == "project" {
				projectLocation = &locations[index]
				break
			}
		}
		if projectLocation == nil {
			c.JSON(500, gin.H{"error": "未找到项目 Skills 目录"})
			return
		}
		if err := restoreProjectBackupSkills(projectLocation.Path); err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("整理项目 Skills 备份失败: %v", err)})
			return
		}

		selected := make(map[string]skillItem)
		duplicates := make(map[string]struct{})
		for _, location := range locations {
			items, err := scanSkillLocation(location)
			if err != nil {
				c.JSON(500, gin.H{"error": fmt.Sprintf("扫描 %s 失败: %v", location.Path, err)})
				return
			}
			for _, item := range items {
				if !item.Valid {
					continue
				}
				if existing, found := selected[item.Name]; found {
					if existing.LocationKey != item.LocationKey {
						duplicates[item.Name] = struct{}{}
					}
					continue
				}
				selected[item.Name] = item
			}
		}

		names := make([]string, 0, len(selected))
		for name := range selected {
			names = append(names, name)
		}
		sort.Strings(names)
		imported := 0
		for _, name := range names {
			item := selected[name]
			if item.LocationKey == "project" {
				continue
			}
			destination := filepath.Join(projectLocation.Path, item.Name)
			if err := copySkillDir(item.Path, destination, true); err != nil {
				c.JSON(500, gin.H{"error": fmt.Sprintf("归档 %s 失败: %v", item.Name, err)})
				return
			}
			imported++
		}
		c.JSON(200, gin.H{"success": true, "total": len(selected), "imported": imported, "duplicates": len(duplicates)})
	}
}

func (s *SkillsAPI) Import() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req skillImportRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "Skill 导入请求无效"})
			return
		}
		if len(req.Targets) == 0 {
			c.JSON(400, gin.H{"error": "至少选择一个安装目标"})
			return
		}
		content, err := base64.StdEncoding.DecodeString(req.ContentBase64)
		if err != nil || len(content) == 0 {
			c.JSON(400, gin.H{"error": "导入文件内容无效"})
			return
		}
		if len(content) > maxSkillImportBytes {
			c.JSON(400, gin.H{"error": "导入文件不能超过 20 MB"})
			return
		}

		s.mu.Lock()
		defer s.mu.Unlock()
		locations, err := managedSkillLocations()
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		locationByKey := make(map[string]skillLocation, len(locations))
		for _, location := range locations {
			locationByKey[location.Key] = location
		}

		files, skillName, err := parseImportedSkill(req.FileName, content)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		targets := ensureProjectSkillTarget(req.Targets)
		for _, target := range targets {
			location, exists := locationByKey[target]
			if !exists {
				c.JSON(400, gin.H{"error": fmt.Sprintf("未知安装目标: %s", target)})
				return
			}
			if location.ReadOnly {
				c.JSON(400, gin.H{"error": fmt.Sprintf("安装目标 %s 为只读目录，请选择用户 Skill 目录", location.Agent)})
				return
			}
			if err := writeSkillFilesForLocation(location, skillName, files); err != nil {
				c.JSON(500, gin.H{"error": fmt.Sprintf("安装到 %s 失败: %v", location.Agent, err)})
				return
			}
		}
		c.JSON(200, gin.H{"success": true, "name": skillName})
	}
}

func (s *SkillsAPI) Delete() gin.HandlerFunc {
	return func(c *gin.Context) {
		ref, ok := bindSkillReference(c)
		if !ok {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if ref.ReadOnly {
			c.JSON(400, gin.H{"error": "该 Skill 来自插件缓存或内置目录，不能直接删除；请通过原 Agent 的插件管理功能移除"})
			return
		}
		if err := os.RemoveAll(ref.Path); err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("删除 Skill 失败: %v", err)})
			return
		}
		c.JSON(200, gin.H{"success": true})
	}
}

// CopySkill 将用户目录中的 Skill（包含 SKILL.md 及其 references/scripts 等资源）复制到其他可写 Agent 目录。
// 插件缓存和内置 Skill 是外部管理的只读来源，不允许作为复制源或复制目标。
func (s *SkillsAPI) Copy() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req skillCopyRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "Skill 复制请求无效"})
			return
		}
		if len(req.Targets) == 0 {
			c.JSON(400, gin.H{"error": "至少选择一个复制目标"})
			return
		}

		s.mu.Lock()
		defer s.mu.Unlock()
		locations, err := managedSkillLocations()
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		locationByKey := make(map[string]skillLocation, len(locations))
		for _, location := range locations {
			locationByKey[location.Key] = location
		}
		sourceLocation, exists := locationByKey[req.LocationKey]
		if !exists {
			c.JSON(400, gin.H{"error": "未知 Skill 来源目录"})
			return
		}
		if sourceLocation.ReadOnly {
			c.JSON(400, gin.H{"error": "内置或插件 Skill 为只读来源，不能复制；请先复制用户目录中的 Skill"})
			return
		}
		if !skillNamePattern.MatchString(req.Name) {
			c.JSON(400, gin.H{"error": "Skill 名称必须为小写字母、数字和连字符"})
			return
		}
		sourcePath := filepath.Join(sourceLocation.Path, req.Name)
		if filepath.Dir(sourcePath) != filepath.Clean(sourceLocation.Path) {
			c.JSON(400, gin.H{"error": "Skill 路径无效"})
			return
		}
		for _, target := range req.Targets {
			location, ok := locationByKey[target]
			if !ok {
				c.JSON(400, gin.H{"error": fmt.Sprintf("未知复制目标: %s", target)})
				return
			}
			if location.ReadOnly {
				c.JSON(400, gin.H{"error": fmt.Sprintf("复制目标 %s 为只读目录", location.Agent)})
				return
			}
			if target == req.LocationKey {
				c.JSON(400, gin.H{"error": "复制目标不能与来源相同"})
				return
			}
		}
		for _, target := range req.Targets {
			location := locationByKey[target]
			destination := filepath.Join(location.Path, req.Name)
			if filepath.Dir(destination) != filepath.Clean(location.Path) {
				c.JSON(400, gin.H{"error": "Skill 目标路径无效"})
				return
			}
			if err := copySkillDir(sourcePath, destination, location.Key == "project"); err != nil {
				c.JSON(500, gin.H{"error": fmt.Sprintf("复制到 %s 失败: %v", location.Agent, err)})
				return
			}
		}
		c.JSON(200, gin.H{"success": true, "name": req.Name, "targets": req.Targets})
	}
}

// BackupSkill 将原文和翻译文本并列保存，并同步项目目录副本，不回写来源 Agent 的 SKILL.md。
func (s *SkillsAPI) Backup() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req skillBackupRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "Skill 备份请求无效"})
			return
		}
		if strings.TrimSpace(req.Translated) == "" {
			c.JSON(400, gin.H{"error": "翻译文本不能为空"})
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		ref, err := resolveSkillReference(req.LocationKey, req.Name)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		original, err := os.ReadFile(filepath.Join(ref.Path, "SKILL.md"))
		if err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("读取原 Skill 失败: %v", err)})
			return
		}
		if req.Original == "" || skillContentSHA256([]byte(req.Original)) != skillContentSHA256(original) {
			c.JSON(409, gin.H{"error": "原 Skill 已变更，请重新打开后再保存译文"})
			return
		}
		backupDir, err := projectSkillsBackupDir(req.Name)
		if err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("创建备份目录失败: %v", err)})
			return
		}
		metadata, err := json.MarshalIndent(skillBackupMetadata{
			Name: req.Name, SourcePath: filepath.Join(ref.Path, "SKILL.md"), SourceSHA256: skillContentSHA256(original), Agent: ref.Agent,
			Model: strings.TrimSpace(req.Model), ChannelName: strings.TrimSpace(req.ChannelName), CreatedAt: time.Now().Format(time.RFC3339),
		}, "", "  ")
		if err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("序列化备份元数据失败: %v", err)})
			return
		}
		if err := os.WriteFile(filepath.Join(backupDir, "original.md"), original, 0600); err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("保存原文备份失败: %v", err)})
			return
		}
		if err := os.WriteFile(filepath.Join(backupDir, "translated.zh-CN.md"), []byte(req.Translated), 0600); err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("保存译文备份失败: %v", err)})
			return
		}
		if err := os.WriteFile(filepath.Join(backupDir, "metadata.json"), append(metadata, '\n'), 0600); err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("保存备份元数据失败: %v", err)})
			return
		}
		// 项目目录是可恢复的统一 Skill 来源。保存翻译时同步保留完整原 Skill，
		// 即使原 Agent 目录之后被删除，也可以从项目目录复制回来。
		if copyErr := copySkillDir(ref.Path, projectSkillRootForName(req.Name), true); copyErr != nil {
			// 源 Skill 缺少附属资源或读取失败时降级为只同步 SKILL.md，保证项目目录始终有副本。
			if writeErr := writeSkillFilesPreserving(projectSkillRootForName(req.Name), map[string][]byte{"SKILL.md": original}); writeErr != nil {
				c.JSON(500, gin.H{"error": fmt.Sprintf("保存项目 Skill 副本失败: %v", writeErr)})
				return
			}
		}
		c.JSON(200, gin.H{"success": true, "path": backupDir})
	}
}

func bindSkillReference(c *gin.Context) (skillItem, bool) {
	var req skillReferenceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "Skill 引用无效"})
		return skillItem{}, false
	}
	ref, err := resolveSkillReference(req.LocationKey, req.Name)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return skillItem{}, false
	}
	return ref, true
}

func resolveSkillReference(locationKey, name string) (skillItem, error) {
	if !skillNamePattern.MatchString(name) {
		return skillItem{}, errors.New("Skill 名称必须为小写字母、数字和连字符")
	}
	locations, err := managedSkillLocations()
	if err != nil {
		return skillItem{}, err
	}
	for _, location := range locations {
		if location.Key == locationKey {
			path := filepath.Join(location.Path, name)
			if filepath.Dir(path) != filepath.Clean(location.Path) {
				return skillItem{}, errors.New("Skill 路径无效")
			}
			if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err != nil {
				return skillItem{}, fmt.Errorf("Skill 不存在: %s", name)
			}
			return skillItem{LocationKey: location.Key, Agent: location.Agent, Name: name, Path: path, SourceType: location.SourceType, ReadOnly: location.ReadOnly}, nil
		}
	}
	return skillItem{}, errors.New("未知 Skill 目录")
}

func managedSkillLocations() ([]skillLocation, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	claudeHome := filepath.Join(home, ".claude")
	if configured := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); configured != "" {
		claudeHome = absolutePath(configured)
	}
	codexHome := filepath.Join(home, ".codex")
	if configured := strings.TrimSpace(os.Getenv("CODEX_HOME")); configured != "" {
		codexHome = absolutePath(configured)
	}
	openCodeDir := filepath.Join(filepath.Dir(resolveOpenCodeConfigPath()), "skills")
	projectDir, err := projectSkillsRoot()
	if err != nil {
		return nil, err
	}
	locations := []skillLocation{
		{Key: "project", Agent: "项目 Skills 目录", Path: projectDir, SourceType: "project", ReadOnly: false},
		{Key: "agents", Agent: "通用 Agent Skills", Path: filepath.Join(home, ".agents", "skills"), SourceType: "user", ReadOnly: false},
		{Key: "claude-code", Agent: "Claude Code 用户目录", Path: filepath.Join(claudeHome, "skills"), SourceType: "user", ReadOnly: false},
		{Key: "codex", Agent: "Codex 用户目录", Path: filepath.Join(codexHome, "skills"), SourceType: "user", ReadOnly: false},
		{Key: "opencode", Agent: "OpenCode 用户目录", Path: openCodeDir, SourceType: "user", ReadOnly: false},
		{Key: "cursor", Agent: "Cursor 用户目录", Path: filepath.Join(home, ".cursor", "skills"), SourceType: "user", ReadOnly: false},
		{Key: "codex-system", Agent: "Codex 内置 Skill", Path: filepath.Join(codexHome, "skills", ".system"), SourceType: "codex-system", ReadOnly: true},
	}

	pluginLocations, err := discoverClaudePluginSkillLocations(claudeHome)
	if err != nil {
		return nil, err
	}
	locations = append(locations, pluginLocations...)
	for index := range locations {
		_, statErr := os.Stat(locations[index].Path)
		locations[index].Exists = statErr == nil
	}
	return locations, nil
}

func discoverClaudePluginSkillLocations(claudeHome string) ([]skillLocation, error) {
	type installedPlugin struct {
		InstallPath string `json:"installPath"`
	}
	type installedPluginsFile struct {
		Plugins map[string][]installedPlugin `json:"plugins"`
	}

	content, err := os.ReadFile(filepath.Join(claudeHome, "plugins", "installed_plugins.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var installed installedPluginsFile
	if err := json.Unmarshal(content, &installed); err != nil {
		return nil, fmt.Errorf("解析 Claude Code 已安装插件清单失败: %w", err)
	}

	locations := make([]skillLocation, 0)
	seen := make(map[string]struct{})
	for pluginID, versions := range installed.Plugins {
		pluginName := strings.SplitN(pluginID, "@", 2)[0]
		for _, plugin := range versions {
			root := filepath.Clean(plugin.InstallPath)
			if root == "." || root == "" {
				continue
			}
			if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
				continue
			} else if err != nil {
				return nil, err
			}
			err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if !entry.IsDir() {
					return nil
				}
				name := strings.ToLower(entry.Name())
				if name == ".git" || name == "node_modules" {
					return fs.SkipDir
				}
				if name != "skills" || filepath.Clean(path) == root {
					return nil
				}
				relative, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				key := "claude-plugin-cache:" + filepath.ToSlash(root) + ":" + filepath.ToSlash(relative)
				if _, exists := seen[key]; !exists {
					seen[key] = struct{}{}
					locations = append(locations, skillLocation{Key: key, Agent: "Claude 插件 · " + pluginName, Path: path, SourceType: "claude-plugin-cache", ReadOnly: true})
				}
				return fs.SkipDir
			})
			if err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(locations, func(i, j int) bool { return locations[i].Key < locations[j].Key })
	return locations, nil
}

// restoreProjectBackupSkills 兼容早期仅保存 original.md 的翻译备份，
// 使其成为项目目录中可查看、可复制的标准 Skill。
func restoreProjectBackupSkills(root string) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !skillNamePattern.MatchString(entry.Name()) {
			continue
		}
		skillDir := filepath.Join(root, entry.Name())
		if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		backupDirs, err := projectSkillBackupDirectories(skillDir)
		if err != nil {
			return err
		}
		for _, backupDir := range backupDirs {
			original, err := os.ReadFile(filepath.Join(backupDir, "original.md"))
			if err != nil {
				continue
			}
			name, _, err := parseSkillFrontmatter(original)
			if err != nil || name != entry.Name() {
				continue
			}
			if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), original, 0600); err != nil {
				return err
			}
			break
		}
	}
	return nil
}

// projectSkillBackupDirectories 同时读取当前 .backups 目录和早期的扁平时间目录。
func projectSkillBackupDirectories(skillDir string) ([]string, error) {
	entries, err := os.ReadDir(skillDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	directories := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() && skillBackupDirectoryPattern.MatchString(entry.Name()) {
			directories = append(directories, filepath.Join(skillDir, entry.Name()))
		}
	}
	backupRoot := filepath.Join(skillDir, skillBackupsDirName)
	backupEntries, err := os.ReadDir(backupRoot)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, entry := range backupEntries {
		if entry.IsDir() && skillBackupDirectoryPattern.MatchString(entry.Name()) {
			directories = append(directories, filepath.Join(backupRoot, entry.Name()))
		}
	}
	sort.Slice(directories, func(i, j int) bool { return filepath.Base(directories[i]) > filepath.Base(directories[j]) })
	return directories, nil
}

func scanSkillLocation(location skillLocation) ([]skillItem, error) {
	entries, err := os.ReadDir(location.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	items := make([]skillItem, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !skillNamePattern.MatchString(entry.Name()) {
			continue
		}
		path := filepath.Join(location.Path, entry.Name())
		content, err := os.ReadFile(filepath.Join(path, "SKILL.md"))
		item := skillItem{LocationKey: location.Key, Agent: location.Agent, Name: entry.Name(), Path: path, SourceType: location.SourceType, ReadOnly: location.ReadOnly}
		if metadata, metadataErr := readSkillGlobalMetadata(item.Name); metadataErr == nil {
			item.Note = metadata.Note
		}
		if err != nil {
			item.Issue = "缺少或无法读取 SKILL.md"
		} else {
			name, description, parseErr := parseSkillFrontmatter(content)
			item.Description = description
			if parseErr != nil || name != entry.Name() {
				item.Issue = "frontmatter 的 name 必须与目录名一致"
			} else {
				item.Valid = true
				if backup, found, backupErr := latestSkillBackup(item, content); backupErr == nil && found {
					item.TranslatedName = translatedSkillDisplayName([]byte(backup.Translated))
				}
			}
		}
		_ = filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			if info, err := entry.Info(); err == nil {
				item.Size += info.Size()
				if info.ModTime().After(timeFromString(item.ModifiedAt)) {
					item.ModifiedAt = info.ModTime().Format(time.RFC3339)
				}
			}
			return nil
		})
		items = append(items, item)
	}
	return items, nil
}

func timeFromString(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339, value)
	return parsed
}

func parseImportedSkill(fileName string, content []byte) (map[string][]byte, string, error) {
	if strings.EqualFold(filepath.Ext(fileName), ".zip") {
		return parseSkillZip(content)
	}
	if !strings.EqualFold(filepath.Ext(fileName), ".md") {
		return nil, "", errors.New("仅支持 ZIP 压缩包或 Markdown 文件")
	}
	name, _, err := parseSkillFrontmatter(content)
	if err != nil {
		return nil, "", err
	}
	return map[string][]byte{"SKILL.md": content}, name, nil
}

func parseSkillZip(content []byte) (map[string][]byte, string, error) {
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, "", errors.New("ZIP 文件无效")
	}
	files := make(map[string][]byte)
	var skillPath string
	for _, file := range reader.File {
		name := filepath.ToSlash(file.Name)
		if strings.HasPrefix(name, "/") || strings.Contains(name, "../") || file.FileInfo().IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Base(name), "SKILL.md") {
			if skillPath != "" {
				return nil, "", errors.New("ZIP 中只能包含一个 SKILL.md")
			}
			skillPath = filepath.Dir(name)
		}
	}
	if skillPath == "" {
		return nil, "", errors.New("ZIP 中缺少 SKILL.md")
	}
	for _, file := range reader.File {
		name := filepath.ToSlash(file.Name)
		if file.FileInfo().IsDir() || strings.HasPrefix(name, "/") || strings.Contains(name, "../") {
			continue
		}
		if skillPath != "." {
			prefix := skillPath + "/"
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			name = strings.TrimPrefix(name, prefix)
		}
		if name == "" {
			continue
		}
		if file.UncompressedSize64 > maxSkillImportBytes {
			return nil, "", errors.New("ZIP 内文件不能超过 20 MB")
		}
		input, err := file.Open()
		if err != nil {
			return nil, "", err
		}
		data, readErr := io.ReadAll(io.LimitReader(input, maxSkillImportBytes+1))
		input.Close()
		if readErr != nil || len(data) > maxSkillImportBytes {
			return nil, "", errors.New("读取 ZIP 内容失败或文件过大")
		}
		files[name] = data
	}
	skill, exists := files["SKILL.md"]
	if !exists {
		return nil, "", errors.New("ZIP 中的 SKILL.md 无效")
	}
	name, _, err := parseSkillFrontmatter(skill)
	if err != nil {
		return nil, "", err
	}
	return files, name, nil
}

func parseSkillFrontmatter(content []byte) (string, string, error) {
	text := string(content)
	if !strings.HasPrefix(text, "---\n") && !strings.HasPrefix(text, "---\r\n") {
		return "", "", errors.New("SKILL.md 必须以 YAML frontmatter 开头")
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	end := -1
	for index, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			end = index + 1
			break
		}
	}
	if end == -1 {
		return "", "", errors.New("SKILL.md 的 YAML frontmatter 没有结束标记")
	}
	var frontmatter struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &frontmatter); err != nil {
		return "", "", fmt.Errorf("SKILL.md 的 YAML frontmatter 无效: %w", err)
	}
	name := strings.TrimSpace(frontmatter.Name)
	description := strings.TrimSpace(frontmatter.Description)
	if !skillNamePattern.MatchString(name) {
		return "", "", errors.New("SKILL.md 缺少合法的 name（小写字母、数字和连字符）")
	}
	if description == "" {
		return "", "", errors.New("SKILL.md 缺少 description")
	}
	return name, description, nil
}

func translatedSkillDisplayName(content []byte) string {
	text := string(content)
	if !strings.HasPrefix(text, "---\n") && !strings.HasPrefix(text, "---\r\n") {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	end := -1
	for index, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			end = index + 1
			break
		}
	}
	if end == -1 {
		return ""
	}
	var frontmatter struct {
		DisplayName string `yaml:"display_name"`
	}
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &frontmatter); err != nil {
		return ""
	}
	return strings.TrimSpace(frontmatter.DisplayName)
}

const (
	// maxSkillCopyFileBytes 限制单次复制单个文件大小，防止误把超大文件塞进 Skill 目录后流式拷贝爆盘。
	maxSkillCopyFileBytes = 30 << 20
	skillBackupsDirName   = ".backups"
)

// copySkillDir 把源 Skill 目录流式拷贝到目标目录，不把文件内容收进内存。
// preserveBackups 为 true 时（用于项目目录）保留翻译备份目录，清理其余旧文件后写入。
// 拒绝符号链接、校验单文件大小、校验目标路径不逃逸，并要求源目录包含 SKILL.md。
func copySkillDir(source, destination string, preserveBackups bool) error {
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !sourceInfo.IsDir() {
		return errors.New("Skill 来源不是目录")
	}

	if err := os.MkdirAll(destination, 0700); err != nil {
		return err
	}
	if preserveBackups {
		if err := cleanSkillDirPreservingBackups(destination); err != nil {
			return err
		}
	} else if err := os.RemoveAll(destination); err != nil {
		return err
	}
	if err := os.MkdirAll(destination, 0700); err != nil {
		return err
	}

	cleanDest := filepath.Clean(destination)
	sawSkillMD := false
	err = filepath.WalkDir(source, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if current != source && (entry.Name() == skillBackupsDirName || skillBackupDirectoryPattern.MatchString(entry.Name())) {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("不支持复制符号链接: %s", entry.Name())
		}
		relative, relErr := filepath.Rel(source, current)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		if relative == "." || strings.HasPrefix(relative, "../") || strings.Contains(relative, "\\") {
			return errors.New("Skill 文件路径无效")
		}
		fileInfo, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if fileInfo.Size() > maxSkillCopyFileBytes {
			return fmt.Errorf("Skill 文件过大: %s", relative)
		}
		target := filepath.Join(cleanDest, filepath.FromSlash(relative))
		if !strings.HasPrefix(target, cleanDest+string(os.PathSeparator)) {
			return errors.New("Skill 文件路径无效")
		}
		if relative == "SKILL.md" {
			sawSkillMD = true
		}
		return copySkillFile(current, target)
	})
	if err != nil {
		return err
	}
	if !sawSkillMD {
		return errors.New("Skill 缺少 SKILL.md")
	}
	return nil
}

// copySkillFile 用 io.Copy 逐文件拷贝，避免把整个 Skill 收进内存。
func copySkillFile(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	destinationFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(destinationFile, sourceFile); err != nil {
		destinationFile.Close()
		return err
	}
	return destinationFile.Close()
}

// cleanSkillDirPreservingBackups 清理目录内的非备份条目，保留新旧格式的翻译备份目录。
func cleanSkillDirPreservingBackups(destination string) error {
	entries, err := os.ReadDir(destination)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == skillBackupsDirName || skillBackupDirectoryPattern.MatchString(entry.Name()) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(destination, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func writeSkillFiles(destination string, files map[string][]byte) error {
	if err := os.RemoveAll(destination); err != nil {
		return err
	}
	if err := os.MkdirAll(destination, 0700); err != nil {
		return err
	}
	for name, content := range files {
		path := filepath.Join(destination, filepath.FromSlash(name))
		if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(destination)+string(os.PathSeparator)) {
			return errors.New("Skill 文件路径无效")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(path, content, 0600); err != nil {
			return err
		}
	}
	return nil
}

func writeSkillFilesForLocation(location skillLocation, name string, files map[string][]byte) error {
	destination := filepath.Join(location.Path, name)
	if location.Key == "project" {
		return writeSkillFilesPreserving(destination, files)
	}
	return writeSkillFiles(destination, files)
}

// writeSkillFilesPreserving 更新项目 Skill 的主体文件，并保留专用翻译备份目录。
func writeSkillFilesPreserving(destination string, files map[string][]byte) error {
	if err := os.MkdirAll(destination, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		// 保留新旧格式的翻译备份，其余文件必须与当前 Skill 包完全一致。
		if entry.Name() == skillBackupsDirName || skillBackupDirectoryPattern.MatchString(entry.Name()) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(destination, entry.Name())); err != nil {
			return err
		}
	}
	for name, content := range files {
		path := filepath.Join(destination, filepath.FromSlash(name))
		if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(destination)+string(os.PathSeparator)) {
			return errors.New("Skill 文件路径无效")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(path, content, 0600); err != nil {
			return err
		}
	}
	return nil
}

func ensureProjectSkillTarget(targets []string) []string {
	result := make([]string, 0, len(targets)+1)
	seen := make(map[string]struct{}, len(targets)+1)
	for _, target := range targets {
		if _, exists := seen[target]; exists {
			continue
		}
		seen[target] = struct{}{}
		result = append(result, target)
	}
	if _, exists := seen["project"]; !exists {
		result = append(result, "project")
	}
	return result
}

func projectSkillsBackupDir(name string) (string, error) {
	root, err := projectSkillsBackupRoot(name)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, time.Now().Format("20060102-150405.000000000"))
	return dir, os.MkdirAll(dir, 0700)
}

func projectSkillsRoot() (string, error) {
	workingDir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if filepath.Base(workingDir) == "backend-go" {
		workingDir = filepath.Dir(workingDir)
	}
	return filepath.Join(workingDir, "skills"), nil
}

func projectSkillMetadataPath(name string) (string, error) {
	if !skillNamePattern.MatchString(name) {
		return "", errors.New("Skill 名称无效")
	}
	root, err := projectSkillsRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".metadata", name+".json"), nil
}

func readSkillGlobalMetadata(name string) (skillGlobalMetadata, error) {
	path, err := projectSkillMetadataPath(name)
	if err != nil {
		return skillGlobalMetadata{}, err
	}
	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return skillGlobalMetadata{}, nil
	}
	if err != nil {
		return skillGlobalMetadata{}, err
	}
	var metadata skillGlobalMetadata
	if err := json.Unmarshal(content, &metadata); err != nil {
		return skillGlobalMetadata{}, err
	}
	return metadata, nil
}

func writeSkillGlobalMetadata(name string, metadata skillGlobalMetadata) error {
	path, err := projectSkillMetadataPath(name)
	if err != nil {
		return err
	}
	content, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, append(content, '\n'), 0600)
}

func projectSkillRootForName(name string) string {
	root, err := projectSkillsRoot()
	if err != nil {
		return filepath.Join("skills", name)
	}
	return filepath.Join(root, name)
}

func projectSkillsBackupRoot(name string) (string, error) {
	if !skillNamePattern.MatchString(name) {
		return "", errors.New("Skill 名称无效")
	}
	root, err := projectSkillsRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name, skillBackupsDirName), nil
}

type skillBackup struct {
	Translated string
	Path       string
	Metadata   skillBackupMetadata
}

func latestSkillBackup(ref skillItem, original []byte) (skillBackup, bool, error) {
	backupRoot, err := projectSkillsBackupRoot(ref.Name)
	if err != nil {
		return skillBackup{}, false, err
	}
	entries, err := projectSkillBackupDirectories(filepath.Dir(backupRoot))
	if err != nil {
		return skillBackup{}, false, err
	}
	expectedPath := filepath.Clean(filepath.Join(ref.Path, "SKILL.md"))
	expectedHash := skillContentSHA256(original)
	for _, backupPath := range entries {
		metadataBytes, err := os.ReadFile(filepath.Join(backupPath, "metadata.json"))
		if err != nil {
			continue
		}
		var metadata skillBackupMetadata
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			continue
		}
		sourceMatches := metadata.SourceSHA256 != "" && metadata.SourceSHA256 == expectedHash
		if metadata.SourceSHA256 == "" {
			sourceMatches = filepath.Clean(metadata.SourcePath) == expectedPath
		}
		if !sourceMatches {
			continue
		}
		if metadata.SourceSHA256 != "" && metadata.SourceSHA256 != expectedHash {
			continue
		}
		translated, err := os.ReadFile(filepath.Join(backupPath, "translated.zh-CN.md"))
		if err != nil || strings.TrimSpace(string(translated)) == "" {
			continue
		}
		return skillBackup{Translated: string(translated), Path: backupPath, Metadata: metadata}, true, nil
	}
	return skillBackup{}, false, nil
}

func skillContentSHA256(content []byte) string {
	sum := sha256.Sum256(content)
	return fmt.Sprintf("%x", sum)
}
