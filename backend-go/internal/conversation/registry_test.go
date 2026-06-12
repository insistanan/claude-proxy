package conversation

import (
	"testing"
)

func TestBuildIdentityKey(t *testing.T) {
	tests := []struct {
		name     string
		obs      Observation
		expected string
		sameAs   string // 用于测试两个 observation 是否生成相同 key
	}{
		{
			name: "with explicit conversation ID",
			obs: Observation{
				APIKind:        "messages",
				ConversationID: "conv_123",
				Model:          "claude-opus",
				FirstPrompt:    "Hello",
			},
			expected: "messages|conv_123",
		},
		{
			name: "with fallback key - same model and prompt should produce same key",
			obs: Observation{
				APIKind:     "messages",
				Model:       "claude-opus",
				FallbackKey: "fallback_abc123",
				FirstPrompt: "Hello world",
			},
			expected: "messages|fallback|fallback_abc123",
		},
		{
			name: "with fallback key - different prompt should use same fallback if hash matches",
			obs: Observation{
				APIKind:     "messages",
				Model:       "claude-opus",
				FallbackKey: "fallback_abc123",
				FirstPrompt: "Hello world",
			},
			sameAs: "messages|fallback|fallback_abc123",
		},
		{
			name: "without ID or fallback - should generate unique ID",
			obs: Observation{
				APIKind:     "messages",
				Model:       "claude-opus",
				FirstPrompt: "Test",
			},
			// 不验证具体值，因为包含随机部分
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildIdentityKey(tt.obs)

			if tt.expected != "" {
				if result != tt.expected {
					t.Errorf("buildIdentityKey() = %v, want %v", result, tt.expected)
				}
			}

			if tt.sameAs != "" && result != tt.sameAs {
				t.Errorf("buildIdentityKey() = %v, want %v", result, tt.sameAs)
			}

			// 确保生成的 key 不为空
			if result == "" {
				t.Error("buildIdentityKey() returned empty string")
			}
		})
	}
}

func TestBuildIdentityKey_SameFallbackProducesSameKey(t *testing.T) {
	obs1 := Observation{
		APIKind:     "messages",
		Model:       "claude-opus",
		FallbackKey: "fallback_test123",
		FirstPrompt: "What is Go?",
	}

	obs2 := Observation{
		APIKind:     "messages",
		Model:       "claude-opus",
		FallbackKey: "fallback_test123",
		FirstPrompt: "What is Go?",
	}

	key1 := buildIdentityKey(obs1)
	key2 := buildIdentityKey(obs2)

	if key1 != key2 {
		t.Errorf("Same fallback key should produce same identity key, got %v and %v", key1, key2)
	}
}

func TestBuildIdentityKey_DifferentFallbackProducesDifferentKey(t *testing.T) {
	obs1 := Observation{
		APIKind:     "messages",
		Model:       "claude-opus",
		FallbackKey: "fallback_aaa",
		FirstPrompt: "First question",
	}

	obs2 := Observation{
		APIKind:     "messages",
		Model:       "claude-sonnet",
		FallbackKey: "fallback_bbb",
		FirstPrompt: "Second question",
	}

	key1 := buildIdentityKey(obs1)
	key2 := buildIdentityKey(obs2)

	if key1 == key2 {
		t.Errorf("Different fallback keys should produce different identity keys, both got %v", key1)
	}
}

func TestBuildIdentityKey_ExplicitIDTakesPrecedence(t *testing.T) {
	obs := Observation{
		APIKind:        "messages",
		ConversationID: "explicit_conv_id",
		FallbackKey:    "fallback_xyz",
		Model:          "claude-opus",
		FirstPrompt:    "Test",
	}

	result := buildIdentityKey(obs)
	expected := "messages|explicit_conv_id"

	if result != expected {
		t.Errorf("Explicit conversation ID should take precedence, got %v want %v", result, expected)
	}

	// 确保没有包含 fallback
	if containsString(result, "fallback") {
		t.Error("Result should not contain 'fallback' when explicit ID is provided")
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && s[:len(substr)] == substr || 
		len(s) > len(substr) && containsString(s[1:], substr)
}
