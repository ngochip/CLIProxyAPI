package test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cache"
	chat_completions "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/claude/openai/chat-completions"
	"github.com/tidwall/gjson"
)

// TestThinkingEnabledAssistantMessageFix kiểm tra việc fix assistant message
// khi thinking enabled nhưng assistant message không bắt đầu bằng thinking block
// 
// Behavior mới: Disable thinking thay vì add empty block
// (Vì Claude không thể regenerate thinking - sẽ báo lỗi nếu gửi empty block)
func TestThinkingEnabledAssistantMessageFix(t *testing.T) {
	// Input: OpenAI request với thinking enabled
	// Assistant message bắt đầu bằng text (không có thinking block)
	openaiRequest := []byte(`{
		"model": "claude-sonnet-4-5-20250929",
		"messages": [
			{
				"role": "user",
				"content": "test token usage"
			},
			{
				"role": "assistant",
				"content": [
					{
						"type": "text",
						"text": "Tôi sẽ giúp bạn test token usage"
					},
					{
						"type": "tool_use",
						"id": "toolu_123",
						"name": "mcp_obsidian-mcp-tools_search_vault_smart",
						"input": {
							"query": "token usage"
						}
					}
				]
			}
		],
		"reasoning_effort": "high"
	}`)

	// Convert OpenAI request to Claude format
	claudeRequest := chat_completions.ConvertOpenAIRequestToClaude("claude-sonnet-4-5-20250929", openaiRequest, true)

	// Parse result
	result := gjson.ParseBytes(claudeRequest)

	// Verify: thinking should be DISABLED (auto-disabled vì assistant không có thinking)
	thinkingType := result.Get("thinking.type").String()
	if thinkingType == "enabled" {
		t.Errorf("Expected thinking to be disabled (assistant has no thinking block), but got thinking.type=%s", thinkingType)
	}

	// Verify: messages array exists
	messages := result.Get("messages")
	if !messages.Exists() || !messages.IsArray() {
		t.Fatal("Messages array not found")
	}

	// Find last assistant message
	messagesArray := messages.Array()
	var lastAssistantMsg gjson.Result
	lastAssistantIdx := -1
	for i := len(messagesArray) - 1; i >= 0; i-- {
		if messagesArray[i].Get("role").String() == "assistant" {
			lastAssistantMsg = messagesArray[i]
			lastAssistantIdx = i
			break
		}
	}

	if lastAssistantIdx == -1 {
		t.Fatal("No assistant message found")
	}

	// Verify: assistant message content exists and không bị modify
	content := lastAssistantMsg.Get("content")
	if !content.Exists() || !content.IsArray() {
		t.Fatal("Assistant message content not found or not an array")
	}

	contentArray := content.Array()
	if len(contentArray) == 0 {
		t.Fatal("Assistant message content is empty")
	}

	// Verify: content không bị thay đổi (vẫn là text, tool_use)
	if len(contentArray) != 2 {
		t.Errorf("Expected 2 content blocks, got %d", len(contentArray))
	}
	
	if contentArray[0].Get("type").String() != "text" {
		t.Errorf("Expected first block to be 'text', got '%s'", contentArray[0].Get("type").String())
	}
	
	if contentArray[1].Get("type").String() != "tool_use" {
		t.Errorf("Expected second block to be 'tool_use', got '%s'", contentArray[1].Get("type").String())
	}

	t.Logf("✓ Thinking auto-disabled when assistant message has no thinking block")
}

// TestThinkingDisabledNoFix kiểm tra rằng khi thinking disabled,
// không có thay đổi nào được apply
func TestThinkingDisabledNoFix(t *testing.T) {
	openaiRequest := []byte(`{
		"model": "claude-4.5-sonnet",
		"messages": [
			{
				"role": "user",
				"content": "Hello"
			},
			{
				"role": "assistant",
				"content": "Hi there!"
			}
		]
	}`)

	claudeRequest := chat_completions.ConvertOpenAIRequestToClaude("claude-4.5-sonnet", openaiRequest, true)
	result := gjson.ParseBytes(claudeRequest)

	// Verify: thinking không được enable
	thinkingType := result.Get("thinking.type").String()
	if thinkingType == "enabled" {
		t.Error("Expected thinking to not be enabled")
	}

	// Verify: assistant message không bị modify
	messages := result.Get("messages").Array()
	for _, msg := range messages {
		if msg.Get("role").String() == "assistant" {
			content := msg.Get("content")
			if content.IsArray() {
				contentArray := content.Array()
				if len(contentArray) > 0 {
					firstType := contentArray[0].Get("type").String()
					if firstType == "thinking" {
						t.Error("Unexpected thinking block when thinking is disabled")
					}
				}
			}
		}
	}

	t.Logf("✓ No thinking block added when thinking is disabled")
}

// TestAssistantWithCachedThinking kiểm tra rằng khi client gửi thinkId marker,
// thinking được restored từ cache và thinking vẫn enabled
func TestAssistantWithCachedThinking(t *testing.T) {
	// Setup: Cache thinking trước (giả sử đây là thinking từ response trước)
	// ThinkingID phải là hex string (chỉ [a-f0-9])
	thinkingID := "abc123def456"
	thinkingText := "Let me analyze this carefully..."
	// Signature phải >= 50 chars để pass validation
	signature := "dGVzdF9zaWduYXR1cmVfZnJvbV9jbGF1ZGVfYXBpXzEyMzQ1Njc4OTA="
	
	cache.CacheThinking(thinkingID, thinkingText, signature)
	defer cache.ClearThinkingCache(thinkingID)
	
	// Client gửi request với thinkId marker (KHÔNG có thinking content)
	openaiRequest := []byte(`{
		"model": "claude-sonnet-4-5-20250929",
		"messages": [
			{
				"role": "user",
				"content": "Continue"
			},
			{
				"role": "assistant",
				"content": "` + "```plaintext:thinkId:" + thinkingID + "```" + `\nI will help you."
			}
		],
		"reasoning_effort": "high"
	}`)

	claudeRequest := chat_completions.ConvertOpenAIRequestToClaude("claude-sonnet-4-5-20250929", openaiRequest, true)
	result := gjson.ParseBytes(claudeRequest)

	// Verify: thinking vẫn enabled (vì có cached thinking với valid signature)
	thinkingType := result.Get("thinking.type").String()
	if thinkingType != "enabled" {
		t.Errorf("Expected thinking.type=enabled (cached thinking restored), got %s", thinkingType)
	}

	// Find assistant message
	messages := result.Get("messages").Array()
	var assistantContent []gjson.Result
	for _, msg := range messages {
		if msg.Get("role").String() == "assistant" {
			content := msg.Get("content")
			if content.IsArray() {
				assistantContent = content.Array()
			}
			break
		}
	}

	if len(assistantContent) == 0 {
		t.Fatal("Assistant message content not found")
	}

	// Verify: thinking block được restored và ở đầu
	if len(assistantContent) < 2 {
		t.Fatalf("Expected at least 2 content blocks (thinking + text), got %d", len(assistantContent))
	}
	
	firstType := assistantContent[0].Get("type").String()
	if firstType != "thinking" {
		t.Errorf("Expected first content block to be 'thinking', got '%s'", firstType)
	}
	
	// Verify: thinking content và signature từ cache
	restoredThinking := assistantContent[0].Get("thinking").String()
	restoredSignature := assistantContent[0].Get("signature").String()
	
	if restoredThinking != thinkingText {
		t.Errorf("Expected thinking text to be restored from cache")
	}
	
	if restoredSignature != signature {
		t.Errorf("Expected signature to be restored from cache")
	}

	t.Logf("✓ Cached thinking restored correctly (thinking enabled)")
}

