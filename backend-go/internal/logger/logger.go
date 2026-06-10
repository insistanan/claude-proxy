package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Config 日志配置
type Config struct {
	// 日志目录
	LogDir string
	// 日志文件名
	LogFile string
	// 单个日志文件最大大小 (MB)
	MaxSize int
	// 保留的旧日志文件最大数量
	MaxBackups int
	// 保留的旧日志文件最大天数
	MaxAge int
	// 是否压缩旧日志文件
	Compress bool
	// 是否同时输出到控制台
	Console bool
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	return &Config{
		LogDir:     "logs",
		LogFile:    "app.log",
		MaxSize:    100, // 100MB
		MaxBackups: 10,
		MaxAge:     7, // 7 days
		Compress:   true,
		Console:    false,
	}
}

// Setup 初始化日志系统
func Setup(cfg *Config) error {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	cfg.LogDir = resolveLogDir(cfg.LogDir)

	// 确保日志目录存在
	if err := os.MkdirAll(cfg.LogDir, 0755); err != nil {
		return fmt.Errorf("创建日志目录失败: %w", err)
	}

	logPath := filepath.Join(cfg.LogDir, cfg.LogFile)

	// 配置 lumberjack 日志轮转
	lumberLogger := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    cfg.MaxSize,
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAge,
		Compress:   cfg.Compress,
		LocalTime:  true,
	}

	var writer io.Writer
	if cfg.Console {
		// 同时输出到控制台和文件
		writer = io.MultiWriter(os.Stdout, lumberLogger)
	} else {
		// 仅输出到文件
		writer = lumberLogger
	}

	// 设置标准库 log 的输出
	log.SetOutput(writer)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	gin.DefaultWriter = writer
	gin.DefaultErrorWriter = writer

	log.Printf("[Logger-Init] 日志系统已初始化")
	log.Printf("[Logger-Init] 日志文件: %s", logPath)
	log.Printf("[Logger-Init] 轮转配置: 最大 %dMB, 保留 %d 个备份, %d 天", cfg.MaxSize, cfg.MaxBackups, cfg.MaxAge)
	startCleanupScheduler(cfg.LogDir, cfg.LogFile, cfg.MaxAge)

	return nil
}

func resolveLogDir(logDir string) string {
	if logDir == "" {
		logDir = DefaultConfig().LogDir
	}
	if filepath.IsAbs(logDir) {
		return logDir
	}

	exePath, err := os.Executable()
	if err == nil {
		return filepath.Join(filepath.Dir(exePath), logDir)
	}
	return logDir
}

func startCleanupScheduler(logDir, activeLogFile string, maxAgeDays int) {
	if maxAgeDays <= 0 {
		return
	}
	cleanupOldLogFiles(logDir, activeLogFile, maxAgeDays)

	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			cleanupOldLogFiles(logDir, activeLogFile, maxAgeDays)
		}
	}()
}

func cleanupOldLogFiles(logDir, activeLogFile string, maxAgeDays int) {
	cutoff := time.Now().Add(-time.Duration(maxAgeDays) * 24 * time.Hour)
	entries, err := os.ReadDir(logDir)
	if err != nil {
		log.Printf("[Logger-Cleanup] 警告: 读取日志目录失败: %v", err)
		return
	}

	activeLogFile = strings.TrimSpace(activeLogFile)
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == activeLogFile || !looksLikeLogFile(entry.Name()) {
			continue
		}

		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.ModTime().After(cutoff) {
			continue
		}

		path := filepath.Join(logDir, entry.Name())
		if err := os.Remove(path); err != nil {
			log.Printf("[Logger-Cleanup] 警告: 删除过期日志失败: %s, error=%v", path, err)
			continue
		}
		log.Printf("[Logger-Cleanup] 已删除过期日志: %s", path)
	}
}

func looksLikeLogFile(name string) bool {
	name = strings.ToLower(name)
	return strings.HasSuffix(name, ".log") ||
		strings.Contains(name, ".log.") ||
		strings.HasSuffix(name, ".log.gz")
}
