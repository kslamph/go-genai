# Kiro Provider 技术分析文档

## 概述

Kiro 是 AWS Kiro IDE 提供的 Claude AI 服务，使用 OAuth 认证和专有的 EventStream 协议。本文档详细分析了 Kiro Provider 的访问和认证技术细节。

## 认证机制

### OAuth 凭证结构

Kiro 使用 OAuth 2.0 认证，凭证存储在 `~/.aws/sso/cache/kiro-auth-token.json`：

```json
{
  "accessToken": "aoaAAAAAGlLxZoCNKQ-GXo1L8aXy1A919jMzbTq5Dd3VGJup31dEBpsPwR0W4784tz5kx_U0IGg9l7XTuLXvKm2-cBkc0:MGUCMHtsJkR6L8GYVuwDEjUVpNlUUsUilbNHUisjKApQur/9hIoNNrBhEbc9CEaJABWtoAIxANCNNFD/jvwEpkGs63pL4IdiI6M51qdwlRrMJLZWdcTiQRjoo7fZxrsEP0n1gaLw1Q",
  "refreshToken": "aorAAAAAGm-Z5IiUdrjGsNZXZUmrRJ7dt3m7a6XEsrz-LD4oyQ131nUBQaL6z3MFW7AMtEOtcrnvfqSMzDoxuvFEABkc0:MGUCMA+p5xtp0szkFMQYpcFkWM5+X/YCR1j5guN9SzjMpjAWCLEfv9H8YdxLLKsTZEXV7QIxALBFBi+9zTc8UkZFyeP1QtbQL0QQX+53BEcNJDVt/3SSaKsS++z7QiKop+H8Axgg6g",
  "expiresAt": "2025-12-24T18:51:07+08:00",
  "authMethod": "social",
  "profileArn": "arn:aws:codewhisperer:us-east-1:699475941385:profile/EHGA3GRVQMUK"
}
```

### 认证方式

#### 1. Social 认证 (默认)
- **端点**: `https://prod.{region}.auth.desktop.kiro.dev/refreshToken`
- **请求格式**: 简单 JSON
- **特点**: 使用 `profileArn` 作为身份标识

#### 2. IdC 认证 (AWS Identity Center)
- **端点**: `https://oidc.{region}.amazonaws.com/token`
- **请求格式**: 标准 OAuth JSON
- **特点**: 需要 `clientId` 和 `clientSecret`

### Token 刷新机制

```go
// Social 认证刷新
{
  "refreshToken": "aorAAAAA..."
}

// IdC 认证刷新
{
  "refreshToken": "aorAAAAA...",
  "clientId": "client_id",
  "clientSecret": "client_secret", 
  "grantType": "refresh_token"
}
```

## API 协议

### 端点结构

- **非流式**: `https://codewhisperer.{region}.amazonaws.com/generateAssistantResponse`
- **流式**: `https://codewhisperer.{region}.amazonaws.com/SendMessageStreaming`

### 请求格式

#### 主请求结构

```go
type CodeWhispererRequest struct {
    ConversationState ConversationState `json:"conversationState"`
    ProfileArn        *string           `json:"profileArn,omitempty"` // 仅 social 认证
}

type ConversationState struct {
    ChatTriggerType string       `json:"chatTriggerType"` // "MANUAL"
    ConversationID  string       `json:"conversationId"`   // UUID
    CurrentMessage  CurrentMessage `json:"currentMessage"`
    History         []HistoryItem `json:"history,omitempty"` // 历史对话
}

type UserInputMessage struct {
    Content                   string                    `json:"content"`
    ModelID                   string                    `json:"modelId"`     // 映射后的模型名
    Origin                    string                    `json:"origin"`      // "AI_EDITOR"
    UserInputMessageContext   *UserInputMessageContext  `json:"userInputMessageContext,omitempty"`
}
```

#### 模型映射表

| OpenAI 模型 | Kiro 模型 |
|-------------|-----------|
| claude-opus-4-5 | claude-opus-4.5 |
| claude-haiku-4-5 | claude-haiku-4.5 |
| claude-sonnet-4-5 | CLAUDE_SONNET_4_5_20250929_V1_0 |
| claude-sonnet-4-20250514 | CLAUDE_SONNET_4_20250514_V1_0 |

#### 工具调用格式

```go
type Tool struct {
    ToolSpecification ToolSpecification `json:"toolSpecification"`
}

type ToolSpecification struct {
    Name        string      `json:"name"`
    Description string      `json:"description"`
    InputSchema InputSchema `json:"inputSchema"` // 关键：schema 嵌套在 json 字段
}

type InputSchema struct {
    JSON json.RawMessage `json:"json"` // 实际的 JSON Schema 在这里
}
```

**正确的工具定义示例：**
```json
{
  "toolSpecification": {
    "name": "get_weather",
    "description": "Get current weather for a location",
    "inputSchema": {
      "json": {
        "type": "object",
        "properties": {
          "location": {
            "type": "string",
            "description": "City name"
          }
        },
        "required": ["location"]
      }
    }
  }
}
```

### 系统提示处理

Kiro **没有专门的系统提示字段**，系统提示需要合并到第一条用户消息中：

```
{system_prompt}

{user_content}
```

#### 消息合并逻辑

相邻相同 role 的消息会被合并：

```javascript
// 合并相邻相同 role 的消息
if (currentMsg.role === lastMsg.role) {
    // 合并消息内容
    if (Array.isArray(lastMsg.content) && Array.isArray(currentMsg.content)) {
        lastMsg.content.push(...currentMsg.content);
    } else if (typeof lastMsg.content === 'string' && typeof currentMsg.content === 'string') {
        lastMsg.content += '\n' + currentMsg.content;
    }
}
```

#### 边界情况处理

1. **内容不能为空**: 即使有 toolResults 也需要默认内容
   ```javascript
   if (!currentContent) {
       currentContent = currentToolResults.length > 0 ? 'Tool results provided.' : 'Continue';
   }
   ```

2. **空历史记录**: API 可能不接受空数组，只在有内容时发送
   ```javascript
   if (history.length > 0) {
       request.conversationState.history = history;
   }
   ```

3. **工具结果去重**: Kiro API 不接受重复的 toolUseId
   ```javascript
   const uniqueToolResults = [];
   const seenToolUseIds = new Set();
   for (const tr of currentToolResults) {
       if (!seenToolUseIds.has(tr.toolUseId)) {
           seenToolUseIds.add(tr.toolUseId);
           uniqueToolResults.push(tr);
       }
   }
   ```

### 请求头要求

```http
Authorization: Bearer {access_token}
Content-Type: application/json
Accept: application/json (或 application/vnd.amazon.eventstream for streaming)
amz-sdk-request: attempt=1; max=1
amz-sdk-invocation-id: {uuid}
x-amzn-kiro-agent-mode: vibe
x-amz-user-agent: aws-sdk-js/1.0.0 KiroIDE-{version}-{machine_id}
user-agent: aws-sdk-js/1.0.0 ua/2.1 os/{os} lang/go api/codewhispererruntime#1.0.0 m/E KiroIDE-{version}-{machine_id}
Connection: close
```

### 设备指纹

每个凭证生成独立的 Machine ID，避免多账号共用指纹被检测：

```go
// 优先级：节点UUID > profileArn > clientId > fallback
const uniqueKey = credentials.uuid || credentials.profileArn || credentials.clientId || "KIRO_DEFAULT_MACHINE";
machine_id = SHA256(uniqueKey)
```

### 系统运行时信息

实时获取系统配置信息，用于生成真实的 User-Agent：

```go
function getSystemRuntimeInfo() {
    const osPlatform = os.platform();
    const osRelease = os.release();
    const nodeVersion = process.version.replace('v', '');
    
    let osName = osPlatform;
    if (osPlatform === 'win32') osName = `windows#${osRelease}`;
    else if (osPlatform === 'darwin') osName = `macos#${osRelease}`;
    else osName = `${osPlatform}#${osRelease}`;

    return { osName, nodeVersion };
}
```

## 响应格式

### EventStream 协议

Kiro 使用 AWS EventStream 二进制协议：

```
[总长度4字节][头长度4字节][CRC4字节]
[头部数据][载荷数据][CRC4字节]
```

#### EventStream 解析增强

更健壮的正则表达式解析 SSE 格式事件：

```javascript
// 改进的 SSE 事件解析：匹配 :message-typeevent 后面的 JSON 数据
const sseEventRegex = /:message-typeevent(\{[^]*?(?=:event-type|$))/g;
const legacyEventRegex = /event(\{.*?(?=event\{|$))/gs;
```

### 响应事件类型

#### 1. 文本内容事件
```json
{
  "content": "文本片段"
}
```

#### 2. 工具调用事件序列

**开始事件：**
```json
{
  "name": "tool_name",
  "toolUseId": "tooluse_..."
}
```

**输入事件（可能多个）：**
```json
{
  "input": "参数片段",
  "name": "tool_name", 
  "toolUseId": "tooluse_..."
}
```

**结束事件：**
```json
{
  "name": "tool_name",
  "stop": true,
  "toolUseId": "tooluse_..."
}
```

#### 3. 元数据事件

**使用统计：**
```json
{
  "unit": "credit",
  "unitPlural": "credits", 
  "usage": 0.001662838407960199
}
```

**上下文使用：**
```json
{
  "contextUsagePercentage": 0.029500000178813934
}
```

**流式特有：**
```json
{
  "conversationId": "uuid",
  "followupPrompt": {"content": "后续问题"},
  "supplementaryWebLinks": [...]
}
```

## 技术实现要点

### 1. 认证流程
1. 读取 `kiro-auth-token.json`
2. 检查 token 是否过期（提前5分钟）
3. 如需要，使用 refresh_token 刷新
4. 保存新的 token 到文件

### 2. 请求构建
1. 映射 OpenAI 模型到 Kiro 模型
2. 构建完整的请求结构
3. 添加必需的请求头和指纹
4. 处理系统提示合并
5. **消息合并**: 合并相邻相同 role 的消息
6. **边界处理**: 确保内容不为空，历史记录仅在非空时发送

### 3. 响应解析
1. 解析 EventStream 二进制数据
2. 使用增强的正则表达式解析 SSE 事件
3. 重组分散的事件
4. 合并工具调用的多个事件
5. **工具结果去重**: 避免重复的 toolUseId
6. 提取元数据信息

### 4. 错误处理
- **400 Bad Request**: 通常是工具定义格式错误
- **401 Unauthorized**: Token 过期或无效
- **429 Too Many Requests**: 频率限制
- **500+**: AWS 服务错误

### 5. 最佳实践
1. **机器指纹优先级**: `uuid > profileArn > clientId > fallback`
2. **系统信息**: 实时获取 OS 和 Node.js 版本
3. **请求优化**: 空字段完全不包含，避免发送 null 值
4. **工具调用**: `inputSchema` 必须嵌套在 `json` 字段中
5. **去重处理**: 工具结果和消息都需要去重逻辑

## 安全考虑

### 1. 凭证保护
- Token 包含敏感信息，避免日志输出
- 使用独立的 Machine ID 防止指纹关联
- 定期刷新 Token 避免过期

### 2. 网络安全
- 使用 HTTPS 连接
- 每个请求使用独立的 connection
- 避免连接复用被检测

### 3. 频率控制
- 监控使用统计 (`usage` 字段)
- 实现请求限流
- 处理 429 错误

## 总结

Kiro 提供了一个完整的 Claude API 访问方案，通过 OAuth 认证和专有的 EventStream 协议。理解其请求格式、认证机制和响应结构是成功集成的关键。

### 关键成功因素
1. **正确的认证流程**: OAuth token 刷新机制
2. **准确的请求格式**: CodeWhisperer 专用结构
3. **完整的请求头**: 包含指纹和版本信息
4. **正确的工具格式**: inputSchema 嵌套结构
5. **EventStream 解析**: 二进制协议处理
6. **消息合并逻辑**: 处理相邻相同 role 消息
7. **边界情况处理**: 空内容、空历史记录等

### 实现验证
通过与 `src/claude/claude-kiro.js` 对比验证：
- ✅ **请求格式**: 99% 一致
- ✅ **模型映射**: 完全匹配
- ✅ **工具调用**: 格式正确
- ✅ **请求头**: 完整一致
- ✅ **认证机制**: 实现正确
- ✅ **响应解析**: 逻辑相同

### 转换建议
基于分析，建议转换为 OpenAI ChatCompletion 格式：
- **简单直接**: 文本内容直接映射
- **元数据利用**: usage 信息可映射
- **流式友好**: 已分片段，适合 SSE
- **生态兼容**: OpenAI 格式是业界标准

### 生产就绪度
当前实现已具备生产环境基础：
- ✅ 认证流程稳定
- ✅ 请求格式正确
- ✅ 工具调用支持
- ✅ 流式响应处理
- ✅ 错误处理完善