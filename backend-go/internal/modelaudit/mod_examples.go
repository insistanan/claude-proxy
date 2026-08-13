package modelaudit

import (
	"embed"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed examples/*/*
var auditModExamplesFS embed.FS

type AuditModExample struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Mode        AuditModAnalysisMode `json:"mode"`
	Files       map[string]string    `json:"files"`
}

type AuditModExamplesResponse struct {
	Examples []AuditModExample `json:"examples"`
}

func BuiltinAuditModExamples() ([]AuditModExample, error) {
	directories, err := fs.ReadDir(auditModExamplesFS, "examples")
	if err != nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取内置 Mod 案例失败", err)
	}
	examples := make([]AuditModExample, 0, len(directories))
	for _, directory := range directories {
		if !directory.IsDir() {
			continue
		}
		root := path.Join("examples", directory.Name())
		entries, err := fs.ReadDir(auditModExamplesFS, root)
		if err != nil {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取内置 Mod 案例目录失败", err)
		}
		hasDirectFile := false
		hasManifest := false
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			hasDirectFile = true
			if entry.Name() == auditModManifestFile {
				hasManifest = true
			}
		}
		if !hasDirectFile {
			// 允许 examples 下并列存放题库等具有更深目录结构的内置资产。
			continue
		}
		if !hasManifest {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "内置 Mod 案例缺少 manifest.json: "+directory.Name())
		}
		files := make(map[string]string)
		err = fs.WalkDir(auditModExamplesFS, root, func(filePath string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			content, readErr := auditModExamplesFS.ReadFile(filePath)
			if readErr != nil {
				return readErr
			}
			relative := strings.TrimPrefix(filePath, root+"/")
			files[relative] = string(content)
			return nil
		})
		if err != nil {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取内置 Mod 案例文件失败", err)
		}
		manifestContent, found := files[auditModManifestFile]
		if !found {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "内置 Mod 案例缺少 manifest.json: "+directory.Name())
		}
		manifest, err := decodeAuditModManifest([]byte(manifestContent))
		if err != nil {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "内置 Mod 案例合同无效", err)
		}
		binaryFiles := make(map[string][]byte, len(files))
		for name, content := range files {
			binaryFiles[name] = []byte(content)
		}
		if err := validateAuditModReferencedFiles(manifest, binaryFiles); err != nil {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "内置 Mod 案例文件无效", err)
		}
		examples = append(examples, AuditModExample{
			ID: manifest.ID, Name: manifest.Name, Description: manifest.Description,
			Mode: manifest.Analysis.Mode, Files: files,
		})
	}
	sort.Slice(examples, func(i, j int) bool { return examples[i].ID < examples[j].ID })
	return examples, nil
}
