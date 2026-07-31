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

var (
	skillsMu         sync.Mutex
	skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
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
	LocationKey string `json:"locationKey"`
	Agent       string `json:"agent"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	ModifiedAt  string `json:"modifiedAt"`
	Valid       bool   `json:"valid"`
	Issue       string `json:"issue,omitempty"`
	SourceType  string `json:"sourceType"`
	ReadOnly    bool   `json:"readOnly"`
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

type skillCopyRequest struct {
	LocationKey string   `json:"locationKey"`
	Name        string   `json:"name"`
	Targets     []string `json:"targets"`
}

// ListSkills 仅枚举当前运行用户的已知 Agent Skill 目录，避免把任意路径暴露为文件浏览器。
func ListSkills() gin.HandlerFunc {
	return func(c *gin.Context) {
		skillsMu.Lock()
		defer skillsMu.Unlock()

		locations, err := managedSkillLocations()
		if err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("解析 Skills 目录失败: %v", err)})
			return
		}

		items := make([]skillItem, 0)
		for _, location := range locations {
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

func GetSkillContent() gin.HandlerFunc {
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

// GetLatestSkillBackup 返回与当前 Skill 来源及内容匹配的最近一份译文。
// 原文内容发生变化时不恢复旧译文，以避免展示与当前版本不对应的翻译。
func GetLatestSkillBackup() gin.HandlerFunc {
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

func ImportSkill() gin.HandlerFunc {
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

		skillsMu.Lock()
		defer skillsMu.Unlock()
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
		for _, target := range req.Targets {
			location, exists := locationByKey[target]
			if !exists {
				c.JSON(400, gin.H{"error": fmt.Sprintf("未知安装目标: %s", target)})
				return
			}
			if location.ReadOnly {
				c.JSON(400, gin.H{"error": fmt.Sprintf("安装目标 %s 为只读目录，请选择用户 Skill 目录", location.Agent)})
				return
			}
			if err := writeSkillFiles(filepath.Join(location.Path, skillName), files); err != nil {
				c.JSON(500, gin.H{"error": fmt.Sprintf("安装到 %s 失败: %v", location.Agent, err)})
				return
			}
		}
		c.JSON(200, gin.H{"success": true, "name": skillName})
	}
}

func DeleteSkill() gin.HandlerFunc {
	return func(c *gin.Context) {
		ref, ok := bindSkillReference(c)
		if !ok {
			return
		}
		skillsMu.Lock()
		defer skillsMu.Unlock()
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
func CopySkill() gin.HandlerFunc {
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

		skillsMu.Lock()
		defer skillsMu.Unlock()
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
		files, err := readSkillFiles(sourcePath)
		if err != nil {
			c.JSON(400, gin.H{"error": fmt.Sprintf("读取待复制 Skill 失败: %v", err)})
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
			if err := writeSkillFiles(filepath.Join(location.Path, req.Name), files); err != nil {
				c.JSON(500, gin.H{"error": fmt.Sprintf("复制到 %s 失败: %v", location.Agent, err)})
				return
			}
		}
		c.JSON(200, gin.H{"success": true, "name": req.Name, "targets": req.Targets})
	}
}

// BackupSkill 将原文和翻译文本并列保存，绝不回写安装目录中的 SKILL.md。
func BackupSkill() gin.HandlerFunc {
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
	claudeHome := home
	if configured := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); configured != "" {
		claudeHome = absolutePath(configured)
	}
	codexHome := filepath.Join(home, ".codex")
	if configured := strings.TrimSpace(os.Getenv("CODEX_HOME")); configured != "" {
		codexHome = absolutePath(configured)
	}
	openCodeDir := filepath.Join(filepath.Dir(resolveOpenCodeConfigPath()), "skills")
	locations := []skillLocation{
		{Key: "claude-code", Agent: "Claude Code 用户目录", Path: filepath.Join(claudeHome, "skills"), SourceType: "user", ReadOnly: false},
		{Key: "codex", Agent: "Codex 用户目录", Path: filepath.Join(codexHome, "skills"), SourceType: "user", ReadOnly: false},
		{Key: "opencode", Agent: "OpenCode 用户目录", Path: openCodeDir, SourceType: "user", ReadOnly: false},
		{Key: "agents", Agent: "通用 Agent Skills", Path: filepath.Join(home, ".agents", "skills"), SourceType: "user", ReadOnly: false},
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
	type pluginRoot struct {
		path       string
		keyPrefix  string
		agent      string
		sourceType string
	}
	roots := []pluginRoot{
		{path: filepath.Join(claudeHome, "plugins", "cache"), keyPrefix: "claude-plugin-cache", agent: "Claude 插件缓存", sourceType: "claude-plugin-cache"},
		{path: filepath.Join(claudeHome, "plugins", "marketplaces"), keyPrefix: "claude-plugin-marketplace", agent: "Claude 插件 Marketplace", sourceType: "claude-plugin-marketplace"},
	}
	locations := make([]skillLocation, 0)
	seen := make(map[string]struct{})
	for _, root := range roots {
		if _, err := os.Stat(root.path); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		err := filepath.WalkDir(root.path, func(path string, entry fs.DirEntry, walkErr error) error {
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
			if name != "skills" || filepath.Clean(path) == filepath.Clean(root.path) {
				return nil
			}
			relative, err := filepath.Rel(root.path, path)
			if err != nil {
				return err
			}
			key := root.keyPrefix + ":" + filepath.ToSlash(relative)
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				locations = append(locations, skillLocation{Key: key, Agent: root.agent, Path: path, SourceType: root.sourceType, ReadOnly: true})
			}
			return fs.SkipDir
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(locations, func(i, j int) bool { return locations[i].Key < locations[j].Key })
	return locations, nil
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
		if err != nil {
			item.Issue = "缺少或无法读取 SKILL.md"
		} else {
			name, description, parseErr := parseSkillFrontmatter(content)
			item.Description = description
			if parseErr != nil || name != entry.Name() {
				item.Issue = "frontmatter 的 name 必须与目录名一致"
			} else {
				item.Valid = true
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

const (
	maxSkillCopyFiles      = 128
	maxSkillCopyTotalBytes = 20 << 20
	maxSkillCopyFileBytes  = 5 << 20
)

func readSkillFiles(source string) (map[string][]byte, error) {
	info, err := os.Stat(source)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("Skill 来源不是目录")
	}
	files := make(map[string][]byte)
	var totalSize int64
	err = filepath.WalkDir(source, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("不支持复制符号链接: %s", entry.Name())
		}
		relative, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "." || strings.HasPrefix(relative, "../") || strings.Contains(relative, "\\") {
			return errors.New("Skill 文件路径无效")
		}
		if len(files) >= maxSkillCopyFiles {
			return fmt.Errorf("Skill 文件数量超过 %d 个", maxSkillCopyFiles)
		}
		fileInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if fileInfo.Size() > maxSkillCopyFileBytes {
			return fmt.Errorf("Skill 文件过大: %s", relative)
		}
		content, err := os.ReadFile(current)
		if err != nil {
			return err
		}
		totalSize += int64(len(content))
		if totalSize > maxSkillCopyTotalBytes {
			return fmt.Errorf("Skill 总大小超过 %d MB", maxSkillCopyTotalBytes/(1<<20))
		}
		files[relative] = content
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, exists := files["SKILL.md"]; !exists {
		return nil, errors.New("Skill 缺少 SKILL.md")
	}
	return files, nil
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

func projectSkillsBackupDir(name string) (string, error) {
	root, err := projectSkillsBackupRoot(name)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, time.Now().Format("20060102-150405.000000000"))
	return dir, os.MkdirAll(dir, 0700)
}

func projectSkillsBackupRoot(name string) (string, error) {
	if !skillNamePattern.MatchString(name) {
		return "", errors.New("Skill 名称无效")
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if filepath.Base(workingDir) == "backend-go" {
		workingDir = filepath.Dir(workingDir)
	}
	return filepath.Join(workingDir, "skills", name), nil
}

type skillBackup struct {
	Translated string
	Path       string
	Metadata   skillBackupMetadata
}

func latestSkillBackup(ref skillItem, original []byte) (skillBackup, bool, error) {
	root, err := projectSkillsBackupRoot(ref.Name)
	if err != nil {
		return skillBackup{}, false, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return skillBackup{}, false, nil
	}
	if err != nil {
		return skillBackup{}, false, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })
	expectedPath := filepath.Clean(filepath.Join(ref.Path, "SKILL.md"))
	expectedHash := skillContentSHA256(original)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		backupPath := filepath.Join(root, entry.Name())
		metadataBytes, err := os.ReadFile(filepath.Join(backupPath, "metadata.json"))
		if err != nil {
			continue
		}
		var metadata skillBackupMetadata
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil || filepath.Clean(metadata.SourcePath) != expectedPath {
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
