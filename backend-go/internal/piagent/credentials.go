package piagent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// CredentialView 是 auth.json 中单个凭据的脱敏视图。
type CredentialView struct {
	ID           string            `json:"id"`
	Type         string            `json:"type"` // api_key / oauth / unknown
	KeyMasked    string            `json:"keyMasked"`
	KeyPresent   bool              `json:"keyPresent"`
	HasOAuth     bool              `json:"hasOAuth"`
	EnvMasked    map[string]string `json:"envMasked"`
	HasEnvValues bool              `json:"hasEnvValues"`
}

// credentialFieldOrder 决定读取凭据时检查的字段顺序。
var credentialFieldOrder = []string{"key", "accessToken", "refreshToken", "clientSecret", "token"}

// ReadCredentials 读取 auth.json 并返回脱敏后的凭据视图。
func (s *Store) ReadCredentials() ([]CredentialView, map[string]json.RawMessage, []byte, bool, error) {
	path, err := s.authPath()
	if err != nil {
		return nil, nil, nil, false, err
	}
	root, raw, exists, err := ReadJSON(path)
	if err != nil {
		return nil, nil, raw, exists, err
	}
	views := make([]CredentialView, 0, len(root))
	for id, rawValue := range root {
		cred := make(map[string]interface{})
		if err := json.Unmarshal(rawValue, &cred); err != nil {
			views = append(views, CredentialView{ID: id, Type: "unknown"})
			continue
		}
		view := credentialView(id, cred)
		views = append(views, view)
	}
	return views, root, raw, exists, nil
}

// ReadRawAuth 读取 auth.json 原始根对象，供探测请求获取真实密钥（不落盘）。
func (s *Store) ReadRawAuth() (map[string]json.RawMessage, []byte, bool, error) {
	path, err := s.authPath()
	if err != nil {
		return nil, nil, false, err
	}
	return ReadJSON(path)
}

// UpdateCredential 以 keep/replace/remove 三态更新 auth.json 中的 API key。
func (s *Store) UpdateCredential(id, action, apiKey, revision string) (WriteResult, error) {
	path, err := s.authPath()
	if err != nil {
		return WriteResult{}, err
	}
	root, raw, exists, err := ReadJSON(path)
	if err != nil {
		return WriteResult{}, err
	}
	if exists && revision == "" {
		return WriteResult{}, ErrRevisionRequired
	}
	if revision != "" && SHA256Hex(raw) != revision {
		return WriteResult{}, ErrRevisionMismatch
	}

	cred, exists := credentialObject(root, id)
	if !exists {
		if action != "replace" {
			return WriteResult{}, fmt.Errorf("凭据不存在: %s", id)
		}
		cred = make(map[string]interface{})
	}
	// OAuth 凭据不能通过 API key 表单覆盖
	if exists && hasOAuthFields(cred) {
		return WriteResult{}, fmt.Errorf("provider %s 使用 OAuth 凭据，无法通过 API Key 表单修改", id)
	}

	switch action {
	case "remove":
		delete(root, id)
	case "replace":
		apiKey = strings.TrimSpace(apiKey)
		if apiKey == "" {
			return WriteResult{}, fmt.Errorf("新的 API Key 不能为空")
		}
		if !strings.Contains(apiKey, "{env:") {
			delete(cred, "env")
		}
		cred["type"] = "api_key"
		cred["key"] = apiKey
		root[id] = mustMarshal(cred)
	default:
		return WriteResult{}, fmt.Errorf("无效的凭据操作: %s", action)
	}

	data, err := MarshalIndent(root)
	if err != nil {
		return WriteResult{}, err
	}
	return WriteFileAtomic(path, data, true, s.BackupDir)
}

// credentialView 构造单个凭据的脱敏视图。
func credentialView(id string, cred map[string]interface{}) CredentialView {
	view := CredentialView{
		ID:        id,
		Type:      credentialType(cred),
		EnvMasked: make(map[string]string),
	}
	apiKey := ""
	for _, field := range credentialFieldOrder {
		if value, ok := cred[field].(string); ok && value != "" {
			if field == "key" {
				apiKey = value
			}
			if field == "accessToken" || field == "refreshToken" {
				view.HasOAuth = true
			}
		}
	}
	if apiKey != "" {
		view.KeyMasked = MaskSecret(apiKey)
		view.KeyPresent = true
	}
	if envValues, ok := cred["env"].(map[string]interface{}); ok {
		for key, value := range envValues {
			if str, ok := value.(string); ok {
				view.EnvMasked[key] = MaskSecret(str)
				view.HasEnvValues = true
			}
		}
	}
	if len(view.EnvMasked) == 0 {
		view.EnvMasked = nil
	}
	return view
}

func credentialType(cred map[string]interface{}) string {
	if hasOAuthFields(cred) {
		return "oauth"
	}
	if value, ok := cred["key"].(string); ok && value != "" {
		return "api_key"
	}
	return "unknown"
}

func hasOAuthFields(cred map[string]interface{}) bool {
	for _, field := range []string{"accessToken", "refreshToken", "oauth", "clientSecret"} {
		if value, ok := cred[field]; ok {
			switch value.(type) {
			case string:
				if value != "" {
					return true
				}
			default:
				return true
			}
		}
	}
	return false
}

func credentialObject(root map[string]json.RawMessage, id string) (map[string]interface{}, bool) {
	raw, exists := root[id]
	if !exists {
		return nil, false
	}
	cred := make(map[string]interface{})
	if err := json.Unmarshal(raw, &cred); err != nil {
		return nil, false
	}
	return cred, true
}

func mustMarshal(value interface{}) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("null")
	}
	return data
}

func (s *Store) authPath() (string, error) {
	dir, err := ResolveConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, string(FileAuth)), nil
}
