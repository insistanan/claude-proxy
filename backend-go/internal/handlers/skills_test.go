package handlers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverClaudePluginSkillLocationsOnlyUsesInstalledPlugins(t *testing.T) {
	claudeHome := t.TempDir()
	installedSkillDir := filepath.Join(claudeHome, "plugins", "cache", "official", "installed-plugin", "1.0.0", "skills")
	marketplaceSkillDir := filepath.Join(claudeHome, "plugins", "marketplaces", "official", "plugins", "available-plugin", "skills")
	if err := os.MkdirAll(installedSkillDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(marketplaceSkillDir, 0700); err != nil {
		t.Fatal(err)
	}
	installedJSON := `{"plugins":{"installed-plugin@official":[{"installPath":"` + filepath.ToSlash(filepath.Dir(installedSkillDir)) + `"}]}}`
	if err := os.WriteFile(filepath.Join(claudeHome, "plugins", "installed_plugins.json"), []byte(installedJSON), 0600); err != nil {
		t.Fatal(err)
	}

	locations, err := discoverClaudePluginSkillLocations(claudeHome)
	if err != nil {
		t.Fatal(err)
	}
	if len(locations) != 1 {
		t.Fatalf("识别到 %d 个目录，期望仅识别已安装插件的 1 个目录: %#v", len(locations), locations)
	}
	if locations[0].Path != installedSkillDir || locations[0].Agent != "Claude 插件 · installed-plugin" {
		t.Fatalf("识别结果错误: %#v", locations[0])
	}
}

func TestWriteSkillFilesPreservingReplacesPackageAndKeepsBackups(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "example-skill")
	backup := filepath.Join(destination, skillBackupsDirName, "20260801-120000.000000000", "translated.zh-CN.md")
	stale := filepath.Join(destination, "scripts", "obsolete.sh")
	if err := os.MkdirAll(filepath.Dir(backup), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(stale), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("旧译文"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("旧脚本"), 0600); err != nil {
		t.Fatal(err)
	}

	files := map[string][]byte{
		"SKILL.md":              []byte("---\nname: example-skill\ndescription: 示例\n---\n"),
		"references/current.md": []byte("当前资源"),
	}
	if err := writeSkillFilesPreserving(destination, files); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("过期资源未清理，err=%v", err)
	}
	if content, err := os.ReadFile(backup); err != nil || string(content) != "旧译文" {
		t.Fatalf("翻译备份未保留，content=%q err=%v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(destination, "references", "current.md")); err != nil || string(content) != "当前资源" {
		t.Fatalf("当前资源未写入，content=%q err=%v", content, err)
	}
}
