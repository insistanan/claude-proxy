package piagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BackupEntry 描述一个备份文件。
type BackupEntry struct {
	ID        string    `json:"id"`
	File      string    `json:"file"` // 备份对应的配置文件类型
	Path      string    `json:"-"`    // 备份文件绝对路径，仅供服务端内部使用
	Size      int64     `json:"size"`
	Created   time.Time `json:"created"`
	Revision  string    `json:"revision"`
	Sensitive bool      `json:"sensitive"`
}

// backupPrefix 从备份文件名解析配置文件类型。
func backupFileKind(name string) (FileKind, bool) {
	for _, kind := range allFileKinds {
		if strings.HasPrefix(name, string(kind)+".") {
			return kind, true
		}
	}
	return "", false
}

// ListBackups 列出备份目录中的所有备份。
func (s *Store) ListBackups() ([]BackupEntry, error) {
	entries, err := os.ReadDir(s.BackupDir)
	if os.IsNotExist(err) {
		return []BackupEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]BackupEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		kind, ok := backupFileKind(entry.Name())
		if !ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(s.BackupDir, entry.Name())
		revision, _ := RevisionOf(path)
		result = append(result, BackupEntry{
			ID:        entry.Name(),
			File:      string(kind),
			Path:      path,
			Size:      info.Size(),
			Created:   info.ModTime(),
			Revision:  revision,
			Sensitive: IsSensitiveFile(kind),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Created.After(result[j].Created)
	})
	return result, nil
}

// CreateBackup 为指定配置文件创建手动快照，返回备份条目。
func (s *Store) CreateBackup(kind FileKind) (BackupEntry, error) {
	dir, err := ResolveConfigDir()
	if err != nil {
		return BackupEntry{}, err
	}
	sourcePath := filepath.Join(dir, string(kind))
	raw, exists, err := ReadRaw(sourcePath)
	if err != nil {
		return BackupEntry{}, err
	}
	if !exists {
		return BackupEntry{}, fmt.Errorf("配置文件不存在: %s", string(kind))
	}
	if err := os.MkdirAll(s.BackupDir, 0700); err != nil {
		return BackupEntry{}, err
	}
	backupPath, err := writeBackup(sourcePath, raw, IsSensitiveFile(kind), s.BackupDir)
	if err != nil {
		return BackupEntry{}, err
	}
	info, err := os.Stat(backupPath)
	if err != nil {
		return BackupEntry{}, err
	}
	return BackupEntry{
		ID:        filepath.Base(backupPath),
		File:      string(kind),
		Path:      backupPath,
		Size:      info.Size(),
		Created:   info.ModTime(),
		Revision:  SHA256Hex(raw),
		Sensitive: IsSensitiveFile(kind),
	}, nil
}

// RestoreBackup 校验备份内容合法后恢复到目标配置文件。
func (s *Store) RestoreBackup(id string) (WriteResult, error) {
	if id == "" || filepath.Base(id) != id || strings.ContainsAny(id, `/\\`) {
		return WriteResult{}, fmt.Errorf("备份文件名无效: %s", id)
	}
	kind, ok := backupFileKind(id)
	if !ok {
		return WriteResult{}, fmt.Errorf("备份文件名无效: %s", id)
	}
	backupPath := filepath.Join(s.BackupDir, id)
	raw, exists, err := ReadRaw(backupPath)
	if err != nil {
		return WriteResult{}, err
	}
	if !exists {
		return WriteResult{}, fmt.Errorf("备份不存在: %s", id)
	}
	// 校验内容为合法 JSON 对象，拒绝损坏备份
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return WriteResult{}, fmt.Errorf("备份内容损坏，无法恢复: %w", err)
	}

	dir, err := ResolveConfigDir()
	if err != nil {
		return WriteResult{}, err
	}
	targetPath := filepath.Join(dir, string(kind))
	result, err := WriteFileAtomic(targetPath, raw, IsSensitiveFile(kind), s.BackupDir)
	if err != nil {
		return WriteResult{}, err
	}
	return result, nil
}
