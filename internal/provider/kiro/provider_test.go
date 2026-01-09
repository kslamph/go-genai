package kiro

import (
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/assert"
)

// Helper to create a ChatMessage
func mkUserMsg(content string) ChatMessage {
	return ChatMessage{Role: "user", Content: content}
}

func mkSystemMsg(content string) ChatMessage {
	return ChatMessage{Role: "system", Content: content}
}

func mkToolResultMsg(toolCallID string, result string) ChatMessage {
	return ChatMessage{
		Role: "user",
		Content: []interface{}{
			map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": toolCallID,
				"content":     result,
			},
		},
	}
}

func mkAssistantMsg(content string) ChatMessage {
	return ChatMessage{Role: "assistant", Content: content}
}

func TestBuildConversationHistory_MergesConsecutiveUserMessages(t *testing.T) {
	// Scenario: User msg -> Assistant msg -> User msg -> Tool Result (User) -> User msg
	msgs := []ChatMessage{
		mkUserMsg("Hello"),
		mkAssistantMsg("Hi"),
		mkUserMsg("Do this"),
		mkToolResultMsg("call_123", "Result"),
		mkUserMsg("And this"),
	}

	history, currentMsg, toolResults := buildConversationHistory(msgs, "claude-haiku-4.5")

	// Preprocessing should merge "Do this", ToolResult, "And this" into one message.
	// So we have:
	// 1. User "Hello"
	// 2. Assistant "Hi"
	// 3. User "Do this...And this" (This becomes CurrentMessage)
	
	// History construction:
	// Item 0: User "Hello"
	// Item 1: Assistant "Hi"
	// Last item is User -> CurrentMessage.
	// Since History ends with Assistant ("Hi"), we DO NOT need to append "Continue".
	
	// So expected history length is 2.

	if len(history) != 2 {
		t.Errorf("Expected 2 history items, got %d", len(history))
		for i, h := range history {
			t.Logf("History %d: User=%v, Assistant=%v", i, h.UserInputMessage != nil, h.AssistantResponseMessage != nil)
		}
	}

	// Check Item 1 (Assistant Hi)
	if len(history) > 1 {
		item1 := history[1].AssistantResponseMessage
		if item1 == nil {
			t.Fatal("Item 1 should be AssistantResponseMessage")
		}
		if item1.Content != "Hi" {
			t.Errorf("Item 1 content mismatch. Got: %s", item1.Content)
		}
	}

	// Check Current Message
	// Content should be "Do this\n\nAnd this" (Tool result content is empty string)
	expectedCurrent := "Do this\n\nAnd this"
	if currentMsg != expectedCurrent {
		t.Errorf("Current message mismatch. Got: %q, Want: %q", currentMsg, expectedCurrent)
	}
	
	// Check tool results in current message
	if len(toolResults) != 1 {
		t.Errorf("Expected 1 tool result in current message, got %d", len(toolResults))
	} else if toolResults[0].ToolUseID != "call_123" {
		t.Errorf("Tool result ID mismatch")
	}
}

func TestBuildConversationHistory_MergesConsecutiveUserContent(t *testing.T) {
	// Scenario: User "Part 1" -> User "Part 2" -> Assistant "Response"
	msgs := []ChatMessage{
		mkUserMsg("Part 1"),
		mkUserMsg("Part 2"),
		mkAssistantMsg("Response"),
	}

	history, currentMsg, _ := buildConversationHistory(msgs, "claude-haiku-4.5")

	// Merged:
	// 1. User "Part 1\n\nPart 2"
	// 2. Assistant "Response" (Last -> Current logic?)
	
	// If last is Assistant:
	// Move Assistant to History.
	// Current = "Continue".
	
	// History:
	// 0. User "Part 1\n\nPart 2"
	// 1. Assistant "Response"
	
	if len(history) != 2 {
		t.Errorf("Expected 2 history items, got %d", len(history))
	}

	// Check Item 0
	item0 := history[0].UserInputMessage
	if item0 == nil {
		t.Fatal("Item 0 should be UserInputMessage")
	}
	if item0.Content != "Part 1\n\nPart 2" {
		t.Errorf("Item 0 content merged incorrectly. Got: %q", item0.Content)
	}
    
    if currentMsg != "Continue" {
		t.Errorf("Expected 'Continue', got %s", currentMsg)
	}
}

func TestBuildConversationHistory_PreservesSystemPrompt(t *testing.T) {
    // Scenario: System msg -> User msg
    msgs := []ChatMessage{
        mkSystemMsg("System Prompt"),
        mkUserMsg("User Prompt"),
    }
    
    _, currentMsg, _ := buildConversationHistory(msgs, "claude-haiku-4.5")
    
    // Expectation: currentMsg should contain "System Prompt\n\nUser Prompt"
    expected := "System Prompt\n\nUser Prompt"
    if currentMsg != expected {
        t.Errorf("System prompt lost. Got: %q, Want: %q", currentMsg, expected)
    }
}

func TestBuildConversationHistory_HandlesEmptyToolResultInHistory(t *testing.T) {
	// Scenario: User -> Assistant -> Tool Result (Empty Text) -> User (Current)
	msgs := []ChatMessage{
		mkUserMsg("Start"),
		mkAssistantMsg("Call tool"),
		mkToolResultMsg("call_1", "Result"),
		mkUserMsg("Next"),
	}

	history, _, _ := buildConversationHistory(msgs, "claude-haiku-4.5")

	// Preprocessing:
	// 1. User "Start"
	// 2. Asst "Call tool"
	// 3. User "Result" (Empty Text) -> Not merged because previous was Asst
	// 4. User "Next" -> Merged with 3!
	
	// So merged list:
	// 1. User "Start"
	// 2. Asst "Call tool"
	// 3. User "Next" (plus Tool Result)
	
	// History:
	// 0. User "Start"
	// 1. Asst "Call tool"
	// Last is User "Next" (merged with Result).
	// History ends with Assistant. No "Continue" inserted.
	
	// Length 2.
	
	if len(history) != 2 {
		t.Errorf("Expected 2 history items, got %d", len(history))
        for i, h := range history {
			t.Logf("History %d: User=%v, Assistant=%v", i, h.UserInputMessage != nil, h.AssistantResponseMessage != nil)
		}
	}
    // No empty content issue because "Next" provided content.
    
    // Let's try a case where it stays empty:
    // User -> Asst -> Tool Result (Last)
    msgs2 := []ChatMessage{
		mkUserMsg("Start"),
		mkAssistantMsg("Call tool"),
		mkToolResultMsg("call_1", "Result"),
    }
    _, currentMsg2, _ := buildConversationHistory(msgs2, "claude-haiku-4.5")
    
    // Merged:
    // 1. User "Start"
    // 2. Asst "Call tool"
    // 3. User (Tool Result)
    
    // Last is User (Tool Result).
    // CurrentMsg = "" (from content).
    // Should be fixed to "Tool results provided."
    if currentMsg2 != "Tool results provided." {
        t.Errorf("Empty current message not fixed. Got: %q", currentMsg2)
    }
}

func TestConvertStreamEvent_PassesRawInput(t *testing.T) {
	state := newKiroStreamState()
	
	// 1. Tool Use Start (to set up state)
	startEvent := StreamEvent{
		Type: "tool_use",
		Content: map[string]interface{}{
			"name":      "test_tool",
			"toolUseId": "call_123",
		},
	}
	_, _, err := convertStreamEventToOpenAIChunk(startEvent, state)
	if err != nil {
		t.Fatalf("Failed to process start event: %v", err)
	}

	// 2. Tool Use Input Delta
	inputEvent := StreamEvent{
		Type: "tool_use_input",
		Content: map[string]interface{}{
			"input": "{\"arg\": \"parti",
		},
	}

	chunk, shouldSend, err := convertStreamEventToOpenAIChunk(inputEvent, state)
	if err != nil {
		t.Fatalf("Failed to convert input event: %v", err)
	}
    
    if !shouldSend {
        t.Error("Expected shouldSend to be true")
    }

	if len(chunk.Choices) == 0 || chunk.Choices[0].Delta.ToolCalls == nil {
		t.Fatal("Missing tool calls in chunk")
	}

	args := chunk.Choices[0].Delta.ToolCalls[0].Function.Arguments
	if args != "{\"arg\": \"parti" {
		t.Errorf("Expected raw args passed through, got: %q", args)
	}
}

// ============ Streaming Event Type Detection Tests ============

func TestGetEventType_DetectsContent(t *testing.T) {
	event := map[string]interface{}{
		"content": "Hello world",
	}
	eventType := getEventType(event)
	assert.Equal(t, "content", eventType)
}

func TestGetEventType_DetectsToolUseStart(t *testing.T) {
	event := map[string]interface{}{
		"name":      "get_weather",
		"toolUseId": "call_123",
	}
	eventType := getEventType(event)
	assert.Equal(t, "tool_use", eventType)
}

func TestGetEventType_DetectsToolUseInput(t *testing.T) {
	event := map[string]interface{}{
		"input": "{\"location\": \"San",
	}
	eventType := getEventType(event)
	assert.Equal(t, "tool_use_input", eventType)
}

func TestGetEventType_DetectsToolUseStop(t *testing.T) {
	event := map[string]interface{}{
		"stop": true,
	}
	eventType := getEventType(event)
	assert.Equal(t, "tool_use_stop", eventType)
}

func TestGetEventType_DetectsUsage(t *testing.T) {
	event := map[string]interface{}{
		"unit": float64(100),
	}
	eventType := getEventType(event)
	assert.Equal(t, "usage", eventType)
}

func TestGetEventType_DetectsContextUsage(t *testing.T) {
	event := map[string]interface{}{
		"contextUsagePercentage": 0.75,
	}
	eventType := getEventType(event)
	assert.Equal(t, "context_usage", eventType)
}

// ============ Stateful Streaming Conversion Tests ============
// Reference Section 2.B: "Do not re-send id or name" in delta chunks

func TestConvertStreamEvent_SendsIdNameOnlyOnce(t *testing.T) {
	state := newKiroStreamState()

	// First event: tool_use with name and id
	startEvent := StreamEvent{
		Type: "tool_use",
		Content: map[string]interface{}{
			"name":      "test_tool",
			"toolUseId": "call_123",
		},
	}
	chunk1, shouldSend1, err := convertStreamEventToOpenAIChunk(startEvent, state)
	assert.NoError(t, err)
	assert.True(t, shouldSend1)

	// Verify first chunk has id, type, name (NO arguments per reference spec)
	tc1 := chunk1.Choices[0].Delta.ToolCalls[0]
	assert.Equal(t, "call_123", tc1.ID)
	assert.Equal(t, "function", tc1.Type)
	assert.Equal(t, "test_tool", tc1.Function.Name)
	// According to reference: first chunk should NOT include arguments
	// Only send arguments when there's actual content
	assert.Equal(t, "", tc1.Function.Arguments, "First chunk should NOT include arguments")

	// Second event: same tool_use (should not send id/name again)
	inputEvent := StreamEvent{
		Type: "tool_use",
		Content: map[string]interface{}{
			"name":      "test_tool", // Should be ignored
			"toolUseId": "call_123",  // Should be ignored
			"input":     "{\"extra\": \"data\"}",
		},
	}
	chunk2, shouldSend2, err := convertStreamEventToOpenAIChunk(inputEvent, state)
	assert.NoError(t, err)
	assert.True(t, shouldSend2)

	// Verify second chunk has ONLY arguments (no id, no type, no name)
	tc2 := chunk2.Choices[0].Delta.ToolCalls[0]
	assert.Empty(t, tc2.ID, "ID should not be resent in delta chunk")
	assert.Equal(t, "", tc2.Type, "Type should not be resent in delta chunk")
	assert.Equal(t, "", tc2.Function.Name, "Name should not be resent in delta chunk")
	assert.Equal(t, "{\"extra\": \"data\"}", tc2.Function.Arguments, "Only arguments should be in delta")
}

func TestConvertStreamEvent_ToolUseInputOnlySendsArguments(t *testing.T) {
	state := newKiroStreamState()
	state.currentToolID = "call_123"
	state.toolCallIndexes["call_123"] = 0
	state.sentToolCallIDs["call_123"] = true

	// Input delta event
	inputEvent := StreamEvent{
		Type: "tool_use_input",
		Content: map[string]interface{}{
			"input": "{\"arg1\": \"val1\", \"arg2\": \"val2\"}",
		},
	}
	chunk, shouldSend, err := convertStreamEventToOpenAIChunk(inputEvent, state)
	assert.NoError(t, err)
	assert.True(t, shouldSend)

	tc := chunk.Choices[0].Delta.ToolCalls[0]
	assert.EqualValues(t, 0, tc.Index)
	assert.Empty(t, tc.ID, "ID should not be in tool_use_input delta")
	assert.Empty(t, tc.Type, "Type should not be in tool_use_input delta")
	assert.Equal(t, "", tc.Function.Name, "Name should not be in tool_use_input delta")
	assert.Equal(t, "{\"arg1\": \"val1\", \"arg2\": \"val2\"}", tc.Function.Arguments)
}

func TestConvertStreamEvent_ToolUseStop_ClearsState(t *testing.T) {
	state := newKiroStreamState()
	state.currentToolID = "call_123"
	state.sentToolCallIDs["call_123"] = true

	stopEvent := StreamEvent{
		Type: "tool_use_stop",
		Content: map[string]interface{}{
			"stop": true,
		},
	}
	_, shouldSend, err := convertStreamEventToOpenAIChunk(stopEvent, state)
	assert.NoError(t, err)
	assert.False(t, shouldSend, "tool_use_stop should not send a chunk")
	assert.Empty(t, state.currentToolID, "currentToolID should be cleared")
}

func TestConvertStreamEvent_ToolUseWithStopFlag(t *testing.T) {
	state := newKiroStreamState()

	// Tool use with stop: true (complete event)
	completeEvent := StreamEvent{
		Type: "tool_use",
		Content: map[string]interface{}{
			"name":      "test_tool",
			"toolUseId": "call_123",
			"stop":      true,
		},
	}
	chunk, shouldSend, err := convertStreamEventToOpenAIChunk(completeEvent, state)
	assert.NoError(t, err)
	assert.True(t, shouldSend)

	// Should still send the tool call header
	tc := chunk.Choices[0].Delta.ToolCalls[0]
	assert.Equal(t, "call_123", tc.ID)
	assert.Equal(t, "test_tool", tc.Function.Name)
}

// ============ Multiple Tool Calls Tests ============

func TestConvertStreamEvent_MultipleToolCalls(t *testing.T) {
	state := newKiroStreamState()

	// First tool
	event1 := StreamEvent{
		Type: "tool_use",
		Content: map[string]interface{}{
			"name":      "tool_1",
			"toolUseId": "call_1",
		},
	}
	chunk1, _, err := convertStreamEventToOpenAIChunk(event1, state)
	assert.NoError(t, err)
	assert.EqualValues(t, 0, chunk1.Choices[0].Delta.ToolCalls[0].Index)

	// Second tool
	event2 := StreamEvent{
		Type: "tool_use",
		Content: map[string]interface{}{
			"name":      "tool_2",
			"toolUseId": "call_2",
		},
	}
	chunk2, _, err := convertStreamEventToOpenAIChunk(event2, state)
	assert.NoError(t, err)
	assert.EqualValues(t, 1, chunk2.Choices[0].Delta.ToolCalls[0].Index)

	// Verify nextToolIndex is 2
	assert.Equal(t, 2, state.nextToolIndex)
}

// ============ Unknown Event Filtering Tests ============

func TestGetEventType_IgnoresFollowupPrompt(t *testing.T) {
	event := map[string]interface{}{
		"followupPrompt": map[string]interface{}{
			"content": "Next question?",
		},
	}
	eventType := getEventType(event)
	// Should return "unknown" since no recognized fields
	assert.Equal(t, "unknown", eventType)
}

func TestGetEventType_IgnoresSupplementaryWebLinks(t *testing.T) {
	event := map[string]interface{}{
		"supplementaryWebLinks": []interface{}{},
	}
	eventType := getEventType(event)
	assert.Equal(t, "unknown", eventType)
}

// ============ End-to-End Streaming State Machine Tests ============

func TestConvertStreamEvent_CompleteToolCallSequence(t *testing.T) {
	state := newKiroStreamState()
	var chunks []openai.ChatCompletionChunk

	// 1. Content chunk
	contentEvent := StreamEvent{
		Type: "content",
		Content: map[string]interface{}{
			"content": "I need to call a tool.",
		},
	}
	chunk, shouldSend, err := convertStreamEventToOpenAIChunk(contentEvent, state)
	assert.NoError(t, err)
	assert.True(t, shouldSend)
	assert.Equal(t, "assistant", chunk.Choices[0].Delta.Role)
	assert.Equal(t, "I need to call a tool.", chunk.Choices[0].Delta.Content)
	chunks = append(chunks, chunk)

	// 2. Tool use start
	toolStartEvent := StreamEvent{
		Type: "tool_use",
		Content: map[string]interface{}{
			"name":      "get_weather",
			"toolUseId": "weather_call",
		},
	}
	chunk, shouldSend, err = convertStreamEventToOpenAIChunk(toolStartEvent, state)
	assert.NoError(t, err)
	assert.True(t, shouldSend)
	assert.Equal(t, "weather_call", chunk.Choices[0].Delta.ToolCalls[0].ID)
	assert.Equal(t, "get_weather", chunk.Choices[0].Delta.ToolCalls[0].Function.Name)
	chunks = append(chunks, chunk)

	// 3. Tool input delta
	toolInputEvent := StreamEvent{
		Type: "tool_use_input",
		Content: map[string]interface{}{
			"input": "{\"location\": \"San Francisco\"}",
		},
	}
	chunk, shouldSend, err = convertStreamEventToOpenAIChunk(toolInputEvent, state)
	assert.NoError(t, err)
	assert.True(t, shouldSend)
	// Should only have arguments
	tc := chunk.Choices[0].Delta.ToolCalls[0]
	assert.Empty(t, tc.ID, "ID should not be in delta")
	assert.Empty(t, tc.Function.Name, "Name should not be in delta")
	assert.Equal(t, "{\"location\": \"San Francisco\"}", tc.Function.Arguments)
	chunks = append(chunks, chunk)

	// 4. Tool use stop
	toolStopEvent := StreamEvent{
		Type: "tool_use_stop",
		Content: map[string]interface{}{
			"stop": true,
		},
	}
	_, shouldSend, err = convertStreamEventToOpenAIChunk(toolStopEvent, state)
	assert.NoError(t, err)
	assert.False(t, shouldSend, "Stop event should not send chunk")

	// 5. More content after tool
	contentEvent2 := StreamEvent{
		Type: "content",
		Content: map[string]interface{}{
			"content": "The weather is nice!",
		},
	}
	chunk, shouldSend, err = convertStreamEventToOpenAIChunk(contentEvent2, state)
	assert.NoError(t, err)
	assert.True(t, shouldSend)
	assert.Equal(t, "The weather is nice!", chunk.Choices[0].Delta.Content)
	chunks = append(chunks, chunk)

	// Verify we sent 4 chunks (not 5, stop was filtered)
	assert.Len(t, chunks, 4, "Expected 4 chunks sent to client")
}

func TestConvertStreamEvent_UsageEvent(t *testing.T) {
	state := newKiroStreamState()

	usageEvent := StreamEvent{
		Type: "usage",
		Content: map[string]interface{}{
			"usage": 0.75,
		},
	}
	chunk, shouldSend, err := convertStreamEventToOpenAIChunk(usageEvent, state)
	assert.NoError(t, err)
	assert.True(t, shouldSend)
	_ = chunk // openai.ChatCompletionChunk doesn't have Usage field for streaming
}

// ============ Edge Case Tests ============

func TestConvertStreamEvent_EmptyContent(t *testing.T) {
	state := newKiroStreamState()

	event := StreamEvent{
		Type: "content",
		Content: map[string]interface{}{
			"content": "",
		},
	}
	chunk, shouldSend, err := convertStreamEventToOpenAIChunk(event, state)
	assert.NoError(t, err)
	// Empty content should still send but with empty string
	assert.True(t, shouldSend)
	assert.Equal(t, "", chunk.Choices[0].Delta.Content)
}

func TestConvertStreamEvent_InitialInputInToolUse(t *testing.T) {
	state := newKiroStreamState()

	// Some implementations send initial input in the tool_use event
	event := StreamEvent{
		Type: "tool_use",
		Content: map[string]interface{}{
			"name":      "test_tool",
			"toolUseId": "call_1",
			"input":     "{\"initial\": true}",
		},
	}
	chunk, shouldSend, err := convertStreamEventToOpenAIChunk(event, state)
	assert.NoError(t, err)
	assert.True(t, shouldSend)

	tc := chunk.Choices[0].Delta.ToolCalls[0]
	assert.Equal(t, "call_1", tc.ID)
	assert.Equal(t, "test_tool", tc.Function.Name)
	// Initial input should be included
	assert.Equal(t, "{\"initial\": true}", tc.Function.Arguments)
}

func TestConvertStreamEvent_MultipleInputChunks(t *testing.T) {
	state := newKiroStreamState()

	// Set up tool call state
	state.currentToolID = "call_1"
	state.toolCallIndexes["call_1"] = 0
	state.sentToolCallIDs["call_1"] = true

	// First input chunk
	event1 := StreamEvent{
		Type: "tool_use_input",
		Content: map[string]interface{}{
			"input": "{\"arg\": \"part1",
		},
	}
	chunk1, shouldSend1, err := convertStreamEventToOpenAIChunk(event1, state)
	assert.NoError(t, err)
	assert.True(t, shouldSend1)
	assert.Equal(t, "{\"arg\": \"part1", chunk1.Choices[0].Delta.ToolCalls[0].Function.Arguments)

	// Second input chunk
	event2 := StreamEvent{
		Type: "tool_use_input",
		Content: map[string]interface{}{
			"input": "part2\"}",
		},
	}
	chunk2, shouldSend2, err := convertStreamEventToOpenAIChunk(event2, state)
	assert.NoError(t, err)
	assert.True(t, shouldSend2)
	assert.Equal(t, "part2\"}", chunk2.Choices[0].Delta.ToolCalls[0].Function.Arguments)
}
