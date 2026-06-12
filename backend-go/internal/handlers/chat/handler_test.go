package chat

import (
	"encoding/json"
	"testing"

	"github.com/BenedictKing/claude-proxy/internal/config"
)

func TestApplyChatModelMapping_DowngradesCustomTool(t *testing.T) {
	input := []byte(`{
		"model":"gpt-4o",
		"stream":true,
		"messages":[{"role":"user","content":"run code"}],
		"tools":[{"type":"custom","name":"code_exec","description":"Execute raw code"}],
		"tool_choice":{"type":"custom","name":"code_exec"}
	}`)

	out, err := applyChatModelMapping(input, &config.UpstreamConfig{})
	if err != nil {
		t.Fatalf("applyChatModelMapping 不应报错: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("解析结果失败: %v", err)
	}

	tools, ok := payload["tools"].([]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("tools 结果异常: %#v", payload["tools"])
	}
	tool, ok := tools[0].(map[string]interface{})
	if !ok {
		t.Fatalf("tool 类型异常: %#v", tools[0])
	}
	if tool["type"] != "function" {
		t.Fatalf("custom tool 应降级为 function，实际: %#v", tool["type"])
	}
	function, ok := tool["function"].(map[string]interface{})
	if !ok {
		t.Fatalf("function 结构异常: %#v", tool["function"])
	}
	if function["name"] != "code_exec" {
		t.Fatalf("function.name 不匹配: %#v", function["name"])
	}
	parameters, ok := function["parameters"].(map[string]interface{})
	if !ok {
		t.Fatalf("parameters 结构异常: %#v", function["parameters"])
	}
	properties, ok := parameters["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("properties 结构异常: %#v", parameters["properties"])
	}
	inputProp, ok := properties["input"].(map[string]interface{})
	if !ok || inputProp["type"] != "string" {
		t.Fatalf("custom tool 应补 input:string schema，实际: %#v", properties["input"])
	}

	toolChoice, ok := payload["tool_choice"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool_choice 类型异常: %#v", payload["tool_choice"])
	}
	if toolChoice["type"] != "function" {
		t.Fatalf("tool_choice.type 应为 function，实际: %#v", toolChoice["type"])
	}
	tcFunction, ok := toolChoice["function"].(map[string]interface{})
	if !ok || tcFunction["name"] != "code_exec" {
		t.Fatalf("tool_choice.function.name 不匹配: %#v", toolChoice["function"])
	}

	streamOptions, ok := payload["stream_options"].(map[string]interface{})
	if !ok || streamOptions["include_usage"] != true {
		t.Fatalf("stream_options.include_usage 应自动补齐，实际: %#v", payload["stream_options"])
	}
}

func TestApplyChatModelMapping_PrunesToolControlsWithoutTools(t *testing.T) {
	input := []byte(`{
		"model":"gpt-4o",
		"messages":[{"role":"user","content":"hi"}],
		"tool_choice":{"type":"function","name":"lookup"},
		"parallel_tool_calls":true
	}`)

	out, err := applyChatModelMapping(input, &config.UpstreamConfig{})
	if err != nil {
		t.Fatalf("applyChatModelMapping 不应报错: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("解析结果失败: %v", err)
	}

	if _, exists := payload["tool_choice"]; exists {
		t.Fatalf("没有 tools 时应移除 tool_choice: %#v", payload["tool_choice"])
	}
	if _, exists := payload["parallel_tool_calls"]; exists {
		t.Fatalf("没有 tools 时应移除 parallel_tool_calls: %#v", payload["parallel_tool_calls"])
	}
}
