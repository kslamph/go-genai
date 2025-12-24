package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/sunbankio/omniproxy/auth"
)

// ============ 数据结构定义 ============

// CodeWhispererRequest 主请求结构
type CodeWhispererRequest struct {
	ConversationState ConversationState `json:"conversationState"`
	ProfileArn        *string           `json:"profileArn,omitempty"`
}

// ConversationState 对话状态
type ConversationState struct {
	ChatTriggerType string         `json:"chatTriggerType"`
	ConversationID  string         `json:"conversationId"`
	CurrentMessage  CurrentMessage `json:"currentMessage"`
	History         []HistoryItem  `json:"history,omitempty"`
}

// CurrentMessage 当前消息
type CurrentMessage struct {
	UserInputMessage UserInputMessage `json:"userInputMessage"`
}

// UserInputMessage 用户输入消息
type UserInputMessage struct {
	Content                 string                   `json:"content"`
	ModelID                 string                   `json:"modelId"`
	Origin                  string                   `json:"origin"`
	UserInputMessageContext *UserInputMessageContext `json:"userInputMessageContext,omitempty"`
}

// UserInputMessageContext 用户输入消息上下文
type UserInputMessageContext struct {
	Tools       []Tool       `json:"tools,omitempty"`
	ToolResults []ToolResult `json:"toolResults,omitempty"`
}

// Tool 工具定义
type Tool struct {
	ToolSpecification ToolSpecification `json:"toolSpecification"`
}

// ToolSpecification 工具规范
type ToolSpecification struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

// InputSchema 输入模式
type InputSchema struct {
	JSON json.RawMessage `json:"json"`
}

// ToolResult 工具结果
type ToolResult struct {
	Content   []TextContent `json:"content"`
	Status    string        `json:"status"`
	ToolUseID string        `json:"toolUseId"`
}

// TextContent 文本内容
type TextContent struct {
	Text string `json:"text"`
}

// HistoryItem 历史记录项
type HistoryItem struct {
	UserInputMessage         *UserInputMessage         `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *AssistantResponseMessage `json:"assistantResponseMessage,omitempty"`
}

// AssistantResponseMessage 助手响应消息
type AssistantResponseMessage struct {
	Content  string    `json:"content"`
	ToolUses []ToolUse `json:"toolUses,omitempty"`
}

// ToolUse 工具使用
type ToolUse struct {
	Input     json.RawMessage `json:"input"`
	Name      string          `json:"name"`
	ToolUseID string          `json:"toolUseId"`
}

// ============ 模型映射表 ============

var modelMap = map[string]string{
	// Opus 4.5 系列
	"claude-opus-4-5":          "claude-opus-4.5",
	"claude-opus-4-5-20251101": "claude-opus-4.5",

	// Haiku 4.5 系列
	"claude-haiku-4-5":          "claude-haiku-4.5",
	"claude-haiku-4-5-20251001": "claude-haiku-4.5",

	// Sonnet 4.5 系列
	"claude-sonnet-4-5":          "CLAUDE_SONNET_4_5_20250929_V1_0",
	"claude-sonnet-4-5-20250929": "CLAUDE_SONNET_4_5_20250929_V1_0",

	// Sonnet 4 系列
	"claude-sonnet-4-20250514": "CLAUDE_SONNET_4_20250514_V1_0",

	// Sonnet 3.7/3.5 系列
	"claude-3-7-sonnet-20250219": "CLAUDE_3_7_SONNET_20250219_V1_0",
	"claude-3-5-sonnet-20241022": "CLAUDE_3_7_SONNET_20250219_V1_0",
	"claude-3-5-sonnet-latest":   "CLAUDE_3_7_SONNET_20250219_V1_0",
}

func getKiroModel(openaiModel string) string {
	if kiroModel, exists := modelMap[openaiModel]; exists {
		return kiroModel
	}
	// 默认返回 Sonnet 4.5
	return "CLAUDE_SONNET_4_5_20250929_V1_0"
}

// ============ 主函数 ============

func main() {
	ctx := context.Background()

	fmt.Println("=== Kiro Correct API Test ===")

	// 初始化认证
	kiroAuth := auth.NewKiroAuthenticator(nil)

	if !kiroAuth.IsAuthenticated() {
		fmt.Println("Authenticating...")
		if err := kiroAuth.Authenticate(ctx); err != nil {
			log.Fatalf("Authentication failed: %v", err)
		}
	}

	token, err := kiroAuth.GetToken(ctx)
	if err != nil {
		log.Fatalf("Failed to get token: %v", err)
	}

	region := kiroAuth.GetRegion()
	fmt.Printf("Using region: %s\n", region)

	// 测试标准请求
	testStandardRequest(ctx, token, region, kiroAuth)

	// 测试工具调用请求
	testToolCallRequest(ctx, token, region, kiroAuth)

	// 测试流式请求
	testStreamRequest(ctx, token, region, kiroAuth)
}

// ============ 测试函数 ============

func testStandardRequest(ctx context.Context, token, region string, kiroAuth *auth.KiroAuthenticator) {
	fmt.Printf("\n=== Testing Standard Request ===\n")

	// 构造请求
	request := buildStandardRequest("Explain Go pointers in one sentence.", "claude-haiku-4-5", kiroAuth)

	sendRequest(ctx, token, region, request, false)
}

func testToolCallRequest(ctx context.Context, token, region string, kiroAuth *auth.KiroAuthenticator) {
	fmt.Printf("\n=== Testing Tool Call Request ===\n")

	// 构造工具定义
	tools := []Tool{
		{
			ToolSpecification: ToolSpecification{
				Name:        "get_weather",
				Description: "Get current weather for a location",
				InputSchema: InputSchema{
					JSON: json.RawMessage(`{
						"type": "object",
						"properties": {
							"location": {
								"type": "string",
								"description": "City name"
							}
						},
						"required": ["location"]
					}`),
				},
			},
		},
	}

	// 构造请求
	request := buildToolCallRequest("What's the weather in Beijing?", "claude-haiku-4-5", tools, kiroAuth)

	sendRequest(ctx, token, region, request, false)
}

func testStreamRequest(ctx context.Context, token, region string, kiroAuth *auth.KiroAuthenticator) {
	fmt.Printf("\n=== Testing Stream Request ===\n")

	// 构造请求
	request := buildStandardRequest("Write a short Go function to reverse a string.", "claude-haiku-4-5", kiroAuth)

	sendRequest(ctx, token, region, request, true)
}

// ============ 请求构建函数 ============

func buildStandardRequest(content, model string, kiroAuth *auth.KiroAuthenticator) *CodeWhispererRequest {
	conversationID := generateUUID()
	kiroModel := getKiroModel(model)

	return &CodeWhispererRequest{
		ConversationState: ConversationState{
			ChatTriggerType: "MANUAL",
			ConversationID:  conversationID,
			CurrentMessage: CurrentMessage{
				UserInputMessage: UserInputMessage{
					Content: content,
					ModelID: kiroModel,
					Origin:  "AI_EDITOR",
				},
			},
		},
		ProfileArn: getProfileArn(kiroAuth),
	}
}

func buildToolCallRequest(content, model string, tools []Tool, kiroAuth *auth.KiroAuthenticator) *CodeWhispererRequest {
	conversationID := generateUUID()
	kiroModel := getKiroModel(model)

	return &CodeWhispererRequest{
		ConversationState: ConversationState{
			ChatTriggerType: "MANUAL",
			ConversationID:  conversationID,
			CurrentMessage: CurrentMessage{
				UserInputMessage: UserInputMessage{
					Content: content,
					ModelID: kiroModel,
					Origin:  "AI_EDITOR",
					UserInputMessageContext: &UserInputMessageContext{
						Tools: tools,
					},
				},
			},
		},
		ProfileArn: getProfileArn(kiroAuth),
	}
}

// ============ HTTP 请求处理 ============

func sendRequest(ctx context.Context, token, region string, request *CodeWhispererRequest, isStream bool) {
	// 序列化请求
	reqBody, err := json.Marshal(request)
	if err != nil {
		log.Printf("Failed to marshal request: %v", err)
		return
	}

	// 确定 URL
	var url string
	if isStream {
		url = fmt.Sprintf("https://codewhisperer.%s.amazonaws.com/SendMessageStreaming", region)
	} else {
		url = fmt.Sprintf("https://codewhisperer.%s.amazonaws.com/generateAssistantResponse", region)
	}

	// 创建 HTTP 请求
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		log.Printf("Failed to create request: %v", err)
		return
	}

	// 设置请求头
	setHeaders(req, token, isStream)

	fmt.Printf("Request URL: %s\n", url)
	fmt.Printf("Request body: %s\n", string(reqBody))

	// 发送请求
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	fmt.Printf("Response Status: %s\n", resp.Status)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("Error response: %s\n", string(body))
		return
	}

	if isStream {
		parseStreamResponse(resp.Body)
	} else {
		parseNormalResponse(resp.Body)
	}
}

func setHeaders(req *http.Request, token string, isStream bool) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("amz-sdk-request", "attempt=1; max=1")
	req.Header.Set("amz-sdk-invocation-id", generateUUID())
	req.Header.Set("x-amzn-kiro-agent-mode", "vibe")
	req.Header.Set("x-amz-user-agent", "aws-sdk-js/1.0.0 KiroIDE-0.7.5-test-"+generateMachineID())
	req.Header.Set("user-agent", "aws-sdk-js/1.0.0 ua/2.1 os/linux lang/go api/codewhispererruntime#1.0.0 m/E")
	req.Header.Set("Connection", "close")

	if isStream {
		req.Header.Set("Accept", "application/vnd.amazon.eventstream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
}

// ============ 响应解析 ============

func parseNormalResponse(body io.Reader) {
	data, err := io.ReadAll(body)
	if err != nil {
		log.Printf("Failed to read response: %v", err)
		return
	}

	fmt.Printf("Response length: %d bytes\n", len(data))
	fmt.Printf("Response body: %s\n", string(data))

	// 解析 EventStream
	parseEventStream(data)
}

func parseStreamResponse(body io.Reader) {
	fmt.Printf("\n--- Parsing Streaming Response ---\n")

	reader := bufio.NewReader(body)
	eventCount := 0

	for {
		// 读取总长度
		lenBuf := make([]byte, 4)
		_, err := io.ReadFull(reader, lenBuf)
		if err != nil {
			if err != io.EOF {
				fmt.Printf("Error reading length: %v\n", err)
			}
			break
		}

		totalLen := binary.BigEndian.Uint32(lenBuf)

		// 读取头长度
		_, err = io.ReadFull(reader, lenBuf)
		if err != nil {
			break
		}
		headerLen := binary.BigEndian.Uint32(lenBuf)

		// 跳过 prelude CRC
		_, err = reader.Discard(4)
		if err != nil {
			break
		}

		// 跳过头部
		_, err = reader.Discard(int(headerLen))
		if err != nil {
			break
		}

		// 读取载荷
		payloadLen := int(totalLen) - 16 - int(headerLen)
		payload := make([]byte, payloadLen)
		_, err = io.ReadFull(reader, payload)
		if err != nil {
			break
		}

		// 跳过 Message CRC
		_, err = reader.Discard(4)
		if err != nil {
			break
		}

		// 解析载荷
		var event map[string]interface{}
		if err := json.Unmarshal(payload, &event); err == nil {
			eventCount++
			fmt.Printf("Stream Event %d: %+v\n", eventCount, event)
		} else {
			fmt.Printf("Stream Event %d: Raw: %s\n", eventCount+1, string(payload[:min(100, len(payload))]))
		}
	}

	fmt.Printf("Total stream events: %d\n", eventCount)
}

func parseEventStream(data []byte) {
	fmt.Printf("\n--- Parsing EventStream ---\n")

	offset := 0
	eventCount := 0

	for offset < len(data) {
		if offset+12 > len(data) {
			break
		}

		// 读取总长度
		totalLen := binary.BigEndian.Uint32(data[offset : offset+4])
		if totalLen < 16 || offset+int(totalLen) > len(data) {
			break
		}

		// 读取头长度
		headerLen := binary.BigEndian.Uint32(data[offset+4 : offset+8])

		// 跳过 prelude 和头部
		payloadStart := offset + 12 + int(headerLen)
		payloadEnd := offset + int(totalLen) - 4

		if payloadStart < payloadEnd && payloadEnd <= len(data) {
			payload := data[payloadStart:payloadEnd]

			// 尝试解析 JSON
			var event map[string]interface{}
			if err := json.Unmarshal(payload, &event); err == nil {
				eventCount++
				fmt.Printf("Event %d: %+v\n", eventCount, event)
			} else {
				fmt.Printf("Event %d: Raw payload (first 100 chars): %s\n", eventCount+1, string(payload[:min(100, len(payload))]))
			}
		}

		offset += int(totalLen)
	}

	fmt.Printf("Total events parsed: %d\n", eventCount)
}

// ============ 辅助函数 ============

func getProfileArn(kiroAuth *auth.KiroAuthenticator) *string {
	if kiroAuth.GetAuthMethod() == "social" {
		if arn := kiroAuth.GetProfileArn(); arn != "" {
			return &arn
		}
	}
	return nil
}

func generateUUID() string {
	return fmt.Sprintf("%x", time.Now().UnixNano())
}

func generateMachineID() string {
	// 简单的机器 ID 生成
	return fmt.Sprintf("%x", time.Now().UnixNano())
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
