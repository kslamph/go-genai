package kiro

import (
	"testing"
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
