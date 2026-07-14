package common

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestExtractConversationID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name   string
		header string
		body   string
		want   string
	}{
		{
			name: "structured Claude Code metadata user ID",
			body: `{"metadata":{"user_id":"{\"session_id\":\"claude-session\"}"}}`,
			want: "claude-session",
		},
		{
			name: "ordinary user ID is not a conversation",
			body: `{"metadata":{"user_id":"user-123"}}`,
			want: "",
		},
		{
			name:   "Codex turn metadata thread ID",
			header: `{"session_id":"installation-123","thread_id":"thread-123"}`,
			body:   `{}`,
			want:   "thread-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			request := httptest.NewRequest("POST", "/v1/responses", nil)
			if tt.header != "" {
				request.Header.Set("X-Codex-Turn-Metadata", tt.header)
			}
			ctx.Request = request
			if got := ExtractConversationID(ctx, []byte(tt.body)); got != tt.want {
				t.Fatalf("ExtractConversationID() = %q, want %q", got, tt.want)
			}
		})
	}
}
