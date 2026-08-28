package config

import (
	"path/filepath"
	"testing"
)

func TestNewEnvConfigDefaultProduction(t *testing.T) {
	t.Setenv("ENV", "")
	t.Setenv("NODE_ENV", "")

	cfg := NewEnvConfig()
	if cfg.Env != "production" {
		t.Fatalf("未设置 ENV/NODE_ENV 时应默认 production，实际 %q", cfg.Env)
	}
	if cfg.IsDevelopment() {
		t.Fatal("默认不应判定为 development")
	}
	if !cfg.IsProduction() {
		t.Fatal("默认应判定为 production")
	}
}

func TestNewEnvConfigPrefersENVOverNodeEnv(t *testing.T) {
	t.Setenv("ENV", "development")
	t.Setenv("NODE_ENV", "production")

	cfg := NewEnvConfig()
	if cfg.Env != "development" {
		t.Fatalf("ENV 应优先于 NODE_ENV，实际 %q", cfg.Env)
	}
}

func TestNewEnvConfigFallsBackToNodeEnv(t *testing.T) {
	t.Setenv("ENV", "")
	t.Setenv("NODE_ENV", "development")

	cfg := NewEnvConfig()
	if cfg.Env != "development" {
		t.Fatalf("ENV 为空时应使用 NODE_ENV，实际 %q", cfg.Env)
	}
}

func TestCandidateDotEnvPathsDedupAndAbsolute(t *testing.T) {
	paths := candidateDotEnvPaths()
	if len(paths) == 0 {
		t.Fatal("至少应包含工作目录 .env 候选")
	}
	seen := make(map[string]struct{}, len(paths))
	for _, envPath := range paths {
		if !filepath.IsAbs(envPath) {
			t.Fatalf("候选路径必须是绝对路径: %s", envPath)
		}
		if filepath.Base(envPath) != ".env" {
			t.Fatalf("候选文件名必须是 .env: %s", envPath)
		}
		if _, exists := seen[envPath]; exists {
			t.Fatalf("重复路径: %s", envPath)
		}
		seen[envPath] = struct{}{}
	}
}
