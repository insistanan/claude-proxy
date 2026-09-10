package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestQuoteTOMLString(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "普通值用字面量字符串", value: "deepseek-flash", want: "'deepseek-flash'"},
		{name: "Windows 路径无需转义", value: `C:\Users\insis\.codex\models.json`, want: `'C:\Users\insis\.codex\models.json'`},
		{name: "含单引号退回基本字符串", value: `O'Brien`, want: `"O'Brien"`},
		{name: "含双引号与反斜杠仍用字面量", value: `a"b\c`, want: `'a"b\c'`},
		{name: "含制表符转义", value: "a\tb", want: `"a\tb"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := quoteTOMLString(tt.value); got != tt.want {
				t.Fatalf("quoteTOMLString(%q) = %s, want %s", tt.value, got, tt.want)
			}
		})
	}
}

func TestMergeCodexRootKey(t *testing.T) {
	value := "deepseek-flash"
	empty := ""
	tests := []struct {
		name    string
		input   string
		key     string
		value   *string
		want    string
		wantErr bool
	}{
		{
			name:  "空文件新增根级键",
			input: "",
			key:   "model",
			value: &value,
			want:  "model = 'deepseek-flash'\n",
		},
		{
			name:  "已有根级键时替换",
			input: "model = 'old'\nmodel_provider = \"ggb\"\n",
			key:   "model",
			value: &value,
			want:  "model = 'deepseek-flash'\nmodel_provider = \"ggb\"\n",
		},
		{
			name:  "插入到首个表头之前",
			input: "model_provider = \"ggb\"\n\n[model_providers.ggb]\nname = \"ggb\"\n",
			key:   "model",
			value: &value,
			want:  "model_provider = \"ggb\"\nmodel = 'deepseek-flash'\n\n[model_providers.ggb]\nname = \"ggb\"\n",
		},
		{
			name:  "插入位置跳过紧贴表头的注释",
			input: "# providers\n[model_providers.ggb]\nname = \"ggb\"\n",
			key:   "model",
			value: &value,
			want:  "model = 'deepseek-flash'\n# providers\n[model_providers.ggb]\nname = \"ggb\"\n",
		},
		{
			name:  "删除已存在的根级键",
			input: "model = 'old'\nmodel_provider = \"ggb\"\n",
			key:   "model",
			value: nil,
			want:  "model_provider = \"ggb\"\n",
		},
		{
			name:  "删除不存在的键为空操作",
			input: "model_provider = \"ggb\"\n",
			key:   "model",
			value: nil,
			want:  "model_provider = \"ggb\"\n",
		},
		{
			name:  "表内同名字段不被误改",
			input: "[model_providers.ggb]\nmodel = 'inner'\n",
			key:   "model",
			value: &value,
			want:  "model = 'deepseek-flash'\n[model_providers.ggb]\nmodel = 'inner'\n",
		},
		{
			name:  "CRLF 行尾保留",
			input: "model = 'old'\r\n\r\n[model_providers.ggb]\r\nname = \"ggb\"\r\n",
			key:   "model",
			value: &value,
			want:  "model = 'deepseek-flash'\r\n\r\n[model_providers.ggb]\r\nname = \"ggb\"\r\n",
		},
		{
			name:  "值为空字符串时写入空字面量",
			input: "model_reasoning_effort = \"high\"\n",
			key:   "model_reasoning_effort",
			value: &empty,
			want:  "model_reasoning_effort = ''\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mergeCodexRootKey([]byte(tt.input), tt.key, tt.value)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际成功: %s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("mergeCodexRootKey 失败: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("结果不符:\ngot:  %q\nwant: %q", got, tt.want)
			}
		})
	}

	t.Run("白名单外的键被拒绝", func(t *testing.T) {
		if _, err := mergeCodexRootKey([]byte("a = 1\n"), "arbitrary_key", &value); err == nil {
			t.Fatal("期望拒绝白名单外的键")
		}
	})
}

func TestCodexModelCatalogUpsert(t *testing.T) {
	t.Run("新增条目包含完整元数据", func(t *testing.T) {
		catalog, err := readCodexModelCatalog(filepath.Join(t.TempDir(), "models.json"))
		if err != nil {
			t.Fatalf("读取空目录失败: %v", err)
		}
		err = catalog.upsertModel(codexModelUpsertInput{
			Template:      "deepseek-flash",
			Slug:          "my-ds",
			SupportsImage: true,
			ContextWindow: 1048576,
		})
		if err != nil {
			t.Fatalf("upsert 失败: %v", err)
		}
		if len(catalog.entries) != 1 {
			t.Fatalf("条目数量错误: %d", len(catalog.entries))
		}
		var entry codexCatalogModelEntry
		if err := json.Unmarshal(catalog.entries[0], &entry); err != nil {
			t.Fatalf("解析条目失败: %v", err)
		}
		if entry.Slug != "my-ds" || entry.DisplayName != "my-ds" {
			t.Fatalf("slug/display_name 错误: %+v", entry)
		}
		if entry.InputModalities[0] != "text" || entry.InputModalities[1] != "image" || !entry.SupportsImageDetailOriginal {
			t.Fatalf("图片输入元数据错误: %+v", entry.InputModalities)
		}
		if entry.ContextWindow != 1048576 || entry.MaxContextWindow != 1048576 {
			t.Fatalf("上下文窗口错误: %d/%d", entry.ContextWindow, entry.MaxContextWindow)
		}
		if entry.DefaultReasoningLevel != "high" {
			t.Fatalf("默认推理档位错误: %s", entry.DefaultReasoningLevel)
		}
		if len(entry.SupportedReasoningLevels) != 3 || entry.TruncationPolicy.Mode != "tokens" || entry.TruncationPolicy.Limit != 10000 {
			t.Fatalf("模板常量字段错误: %+v", entry)
		}
		if entry.ApplyPatchToolType != "freeform" || entry.ShellType != "shell_command" || entry.Priority != 1 {
			t.Fatalf("模板常量字段错误: %+v", entry)
		}
	})

	t.Run("新增时缺少模板报错", func(t *testing.T) {
		catalog, _ := readCodexModelCatalog(filepath.Join(t.TempDir(), "models.json"))
		err := catalog.upsertModel(codexModelUpsertInput{Slug: "x", ContextWindow: 128000})
		if err == nil {
			t.Fatal("期望缺少模板时报错")
		}
	})

	t.Run("更新仅覆盖可调字段并保留未知字段", func(t *testing.T) {
		modelsPath := filepath.Join(t.TempDir(), "models.json")
		seed := `{"models":[{"slug":"my-ds","display_name":"Old","description":"old desc","custom_field":42,"input_modalities":["text"],"context_window":64000,"max_context_window":64000,"default_reasoning_level":"low","supported_reasoning_levels":[{"effort":"low","description":""},{"effort":"high","description":""}]}],"extra":"keep"}`
		if err := os.WriteFile(modelsPath, []byte(seed), 0600); err != nil {
			t.Fatalf("写入种子文件失败: %v", err)
		}
		catalog, err := readCodexModelCatalog(modelsPath)
		if err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		err = catalog.upsertModel(codexModelUpsertInput{
			Slug:                  "my-ds",
			DisplayName:           "New Name",
			SupportsImage:         true,
			ContextWindow:         128000,
			DefaultReasoningLevel: "high",
		})
		if err != nil {
			t.Fatalf("upsert 失败: %v", err)
		}
		var entry map[string]any
		if err := json.Unmarshal(catalog.entries[0], &entry); err != nil {
			t.Fatalf("解析条目失败: %v", err)
		}
		if entry["display_name"] != "New Name" {
			t.Fatalf("display_name 未更新: %v", entry["display_name"])
		}
		if entry["description"] != "old desc" {
			t.Fatalf("未提供的描述应保留原值: %v", entry["description"])
		}
		if entry["custom_field"] != float64(42) {
			t.Fatalf("未知字段被丢弃: %v", entry["custom_field"])
		}
		modalities := entry["input_modalities"].([]any)
		if len(modalities) != 2 || modalities[1] != "image" || entry["supports_image_detail_original"] != true {
			t.Fatalf("图片元数据未更新: %v", modalities)
		}
		if entry["context_window"] != float64(128000) || entry["max_context_window"] != float64(128000) {
			t.Fatalf("上下文窗口未同步提升: %v/%v", entry["context_window"], entry["max_context_window"])
		}
		if entry["default_reasoning_level"] != "high" {
			t.Fatalf("推理档位未更新: %v", entry["default_reasoning_level"])
		}

		encoded, err := catalog.encode()
		if err != nil {
			t.Fatalf("编码失败: %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(encoded, &doc); err != nil {
			t.Fatalf("编码结果不是合法 JSON: %v", err)
		}
		if doc["extra"] != "keep" {
			t.Fatalf("顶层未知字段被丢弃: %v", doc["extra"])
		}
		if !strings.Contains(string(encoded), "\n  \"models\"") {
			t.Fatalf("输出应为缩进 JSON: %s", encoded)
		}
	})

	t.Run("更新时使用不支持的推理档位报错", func(t *testing.T) {
		modelsPath := filepath.Join(t.TempDir(), "models.json")
		seed := `{"models":[{"slug":"my-ds","supported_reasoning_levels":[{"effort":"low","description":""}],"context_window":64000}]}`
		if err := os.WriteFile(modelsPath, []byte(seed), 0600); err != nil {
			t.Fatalf("写入种子文件失败: %v", err)
		}
		catalog, err := readCodexModelCatalog(modelsPath)
		if err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		err = catalog.upsertModel(codexModelUpsertInput{
			Slug:                  "my-ds",
			ContextWindow:         64000,
			DefaultReasoningLevel: "max",
		})
		if err == nil {
			t.Fatal("期望不支持的推理档位报错")
		}
	})
}

func performCodexModelRequest(t *testing.T, handler gin.HandlerFunc, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	var payload []byte
	switch typed := body.(type) {
	case nil:
		payload = nil
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			t.Fatalf("构造请求体失败: %v", err)
		}
		payload = encoded
	}
	context.Request = httptest.NewRequest(method, target, bytes.NewReader(payload))
	context.Request.Header.Set("Content-Type", "application/json")
	handler(context)
	return recorder
}

func TestSaveCodexModelCatalogHandler(t *testing.T) {
	handler := SaveCodexModelCatalog()

	t.Run("upsert 新模型并写入 models.json", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("CODEX_HOME", home)
		recorder := performCodexModelRequest(t, handler, http.MethodPut, "/settings/codex/model-catalog", map[string]any{
			"action":        "upsert",
			"template":      "generic",
			"slug":          "my-model",
			"displayName":   "My Model",
			"contextWindow": 128000,
		})
		if recorder.Code != http.StatusOK {
			t.Fatalf("状态码错误: %d %s", recorder.Code, recorder.Body.String())
		}
		raw, err := os.ReadFile(filepath.Join(home, "models.json"))
		if err != nil {
			t.Fatalf("models.json 未写入: %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("models.json 不是合法 JSON: %v", err)
		}
		models := doc["models"].([]any)
		if len(models) != 1 {
			t.Fatalf("模型数量错误: %d", len(models))
		}
		if models[0].(map[string]any)["slug"] != "my-model" {
			t.Fatalf("slug 错误: %v", models[0])
		}
	})

	t.Run("setDefault 写入根级模型字段且保留 provider 配置", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("CODEX_HOME", home)
		configSeed := "# codex config\nmodel_provider = \"ggb\"\n\n[model_providers.ggb]\nname = \"ggb\"\nbase_url = \"http://localhost:9996/v1\"\n"
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(configSeed), 0600); err != nil {
			t.Fatalf("写入种子配置失败: %v", err)
		}
		upsertRecorder := performCodexModelRequest(t, handler, http.MethodPut, "/settings/codex/model-catalog", map[string]any{
			"action":                "upsert",
			"template":              "deepseek-flash",
			"slug":                  "my-ds",
			"supportsImage":         true,
			"contextWindow":         1048576,
			"defaultReasoningLevel": "high",
		})
		if upsertRecorder.Code != http.StatusOK {
			t.Fatalf("upsert 状态码错误: %d %s", upsertRecorder.Code, upsertRecorder.Body.String())
		}
		recorder := performCodexModelRequest(t, handler, http.MethodPut, "/settings/codex/model-catalog", map[string]any{
			"action":          "setDefault",
			"slug":            "my-ds",
			"reasoningEffort": "high",
		})
		if recorder.Code != http.StatusOK {
			t.Fatalf("setDefault 状态码错误: %d %s", recorder.Code, recorder.Body.String())
		}
		raw, err := os.ReadFile(filepath.Join(home, "config.toml"))
		if err != nil {
			t.Fatalf("读取 config.toml 失败: %v", err)
		}
		updated := string(raw)
		for _, want := range []string{
			"model = 'my-ds'",
			"model_catalog_json = '" + filepath.Join(home, "models.json") + "'",
			"model_reasoning_effort = 'high'",
			"# codex config",
			"model_provider = \"ggb\"",
			"base_url = \"http://localhost:9996/v1\"",
		} {
			if !strings.Contains(updated, want) {
				t.Fatalf("config.toml 缺少 %q:\n%s", want, updated)
			}
		}
	})

	t.Run("clearReasoningEffort 移除推理档位", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("CODEX_HOME", home)
		if err := performCodexUpsertAndDefault(t, handler, home); err != nil {
			t.Fatalf("准备阶段失败: %v", err)
		}
		recorder := performCodexModelRequest(t, handler, http.MethodPut, "/settings/codex/model-catalog", map[string]any{
			"action":               "setDefault",
			"slug":                 "my-ds",
			"clearReasoningEffort": true,
		})
		if recorder.Code != http.StatusOK {
			t.Fatalf("状态码错误: %d %s", recorder.Code, recorder.Body.String())
		}
		raw, err := os.ReadFile(filepath.Join(home, "config.toml"))
		if err != nil {
			t.Fatalf("读取 config.toml 失败: %v", err)
		}
		if strings.Contains(string(raw), "model_reasoning_effort") {
			t.Fatalf("推理档位应被移除:\n%s", raw)
		}
		if !strings.Contains(string(raw), "model = 'my-ds'") {
			t.Fatalf("model 字段应保留:\n%s", raw)
		}
	})

	t.Run("delete 移除条目且删除不存在的模型报错", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("CODEX_HOME", home)
		recorder := performCodexModelRequest(t, handler, http.MethodPut, "/settings/codex/model-catalog", map[string]any{
			"action":        "upsert",
			"template":      "generic",
			"slug":          "my-model",
			"contextWindow": 128000,
		})
		if recorder.Code != http.StatusOK {
			t.Fatalf("upsert 状态码错误: %d %s", recorder.Code, recorder.Body.String())
		}
		recorder = performCodexModelRequest(t, handler, http.MethodPut, "/settings/codex/model-catalog", map[string]any{
			"action": "delete",
			"slug":   "my-model",
		})
		if recorder.Code != http.StatusOK {
			t.Fatalf("delete 状态码错误: %d %s", recorder.Code, recorder.Body.String())
		}
		raw, err := os.ReadFile(filepath.Join(home, "models.json"))
		if err != nil {
			t.Fatalf("读取 models.json 失败: %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("models.json 不是合法 JSON: %v", err)
		}
		if models := doc["models"].([]any); len(models) != 0 {
			t.Fatalf("模型应已删除: %v", models)
		}

		recorder = performCodexModelRequest(t, handler, http.MethodPut, "/settings/codex/model-catalog", map[string]any{
			"action": "delete",
			"slug":   "not-exist",
		})
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("删除不存在的模型应返回 400: %d %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("非法请求被拒绝", func(t *testing.T) {
		t.Setenv("CODEX_HOME", t.TempDir())
		tests := []struct {
			name string
			body map[string]any
		}{
			{name: "未知 action", body: map[string]any{"action": "hack", "slug": "x"}},
			{name: "slug 为空", body: map[string]any{"action": "delete", "slug": "  "}},
			{name: "slug 含非法字符", body: map[string]any{"action": "delete", "slug": "bad slug"}},
			{name: "未知模板", body: map[string]any{"action": "upsert", "template": "nope", "slug": "x", "contextWindow": 1000}},
			{name: "上下文窗口越界", body: map[string]any{"action": "upsert", "template": "generic", "slug": "x", "contextWindow": 0}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				recorder := performCodexModelRequest(t, handler, http.MethodPut, "/settings/codex/model-catalog", tt.body)
				if recorder.Code != http.StatusBadRequest {
					t.Fatalf("应返回 400: %d %s", recorder.Code, recorder.Body.String())
				}
			})
		}
	})
}

func performCodexUpsertAndDefault(t *testing.T, handler gin.HandlerFunc, home string) error {
	t.Helper()
	recorder := performCodexModelRequest(t, handler, http.MethodPut, "/settings/codex/model-catalog", map[string]any{
		"action":        "upsert",
		"template":      "generic",
		"slug":          "my-ds",
		"contextWindow": 128000,
	})
	if recorder.Code != http.StatusOK {
		return fmt.Errorf("upsert 失败: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = performCodexModelRequest(t, handler, http.MethodPut, "/settings/codex/model-catalog", map[string]any{
		"action":          "setDefault",
		"slug":            "my-ds",
		"reasoningEffort": "high",
	})
	if recorder.Code != http.StatusOK {
		return fmt.Errorf("setDefault 失败: %d %s", recorder.Code, recorder.Body.String())
	}
	return nil
}

func TestGetCodexSettingsWithCatalog(t *testing.T) {
	handler := GetCodexSettings()

	t.Run("返回模型目录与模板", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("CODEX_HOME", home)
		modelsPath := filepath.Join(home, "models.json")
		seed := `{"models":[{"slug":"my-ds","display_name":"My DS","input_modalities":["text","image"],"context_window":1048576,"max_context_window":1048576,"default_reasoning_level":"high","supported_reasoning_levels":[{"effort":"low","description":"a"},{"effort":"high","description":"b"}]}]}`
		if err := os.WriteFile(modelsPath, []byte(seed), 0600); err != nil {
			t.Fatalf("写入种子文件失败: %v", err)
		}
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("model = 'my-ds'\nmodel_reasoning_effort = \"high\"\n"), 0600); err != nil {
			t.Fatalf("写入种子配置失败: %v", err)
		}

		recorder := performCodexModelRequest(t, handler, http.MethodGet, "/settings/codex", nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("状态码错误: %d %s", recorder.Code, recorder.Body.String())
		}
		var response codexSettingsResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		if !response.ModelsExists || response.Model != "my-ds" || response.ModelReasoningEffort != "high" {
			t.Fatalf("根级模型字段错误: %+v", response)
		}
		if len(response.CatalogModels) != 1 {
			t.Fatalf("模型条目数量错误: %d", len(response.CatalogModels))
		}
		model := response.CatalogModels[0]
		if model.Slug != "my-ds" || !model.SupportsImage || model.ContextWindow != 1048576 {
			t.Fatalf("模型视图错误: %+v", model)
		}
		if len(model.ReasoningLevels) != 2 || model.ReasoningLevels[0] != "low" {
			t.Fatalf("推理档位视图错误: %+v", model.ReasoningLevels)
		}
		if len(response.ModelTemplates) != 3 {
			t.Fatalf("模板数量错误: %d", len(response.ModelTemplates))
		}
	})

	t.Run("models.json 损坏时通过 modelsError 透出", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("CODEX_HOME", home)
		if err := os.WriteFile(filepath.Join(home, "models.json"), []byte("{invalid"), 0600); err != nil {
			t.Fatalf("写入损坏文件失败: %v", err)
		}
		recorder := performCodexModelRequest(t, handler, http.MethodGet, "/settings/codex", nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("损坏的 models.json 不应阻断设置页: %d %s", recorder.Code, recorder.Body.String())
		}
		var response codexSettingsResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		if response.ModelsError == "" {
			t.Fatal("应包含 modelsError")
		}
		if len(response.CatalogModels) != 0 {
			t.Fatalf("损坏时应返回空模型列表: %v", response.CatalogModels)
		}
	})
}
