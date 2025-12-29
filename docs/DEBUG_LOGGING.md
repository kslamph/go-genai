# Debug Logging for Thought Signature Issues

## How to Enable Debug Logging

There are two ways to enable debug logging:

### Option 1: Using Command Line Flag
```bash
./omniproxy --debug
```

### Option 2: Using Environment Variable
```bash
DEBUG=1 ./omniproxy
# or
export DEBUG=1
./omniproxy
```

## Where Debug Logs Are Stored

When debug mode is enabled, debug logs are written to:
- **File**: `server.log` in the current directory
- **Console**: Info, warn, and error logs still appear on console

## What Gets Logged

The debug logging tracks the entire thought_signature conversion flow:

### 1. **Incoming Request from Client** (ToGeminiRequest)
- Full OpenAI request JSON
- Tool calls with any `extra_content` fields
- Extraction of `thought_signature` from `extra_content.google.thought_signature`

### 2. **Outgoing Request to Gemini** (ToGeminiRequest)
- Gemini Content structure
- Parts with function calls
- Whether `thought_signature` is present in each Part

### 3. **Incoming Response from Gemini** (FromGeminiResponse)
- Gemini response parts
- Function calls with `thought_signature` from Gemini
- Thought signature length and preview

### 4. **Outgoing Response to Client** (FromGeminiResponse)
- Full OpenAI response JSON
- Tool calls with `extra_content` containing `thought_signature`

## Key Log Messages to Look For

### ✓ Success Messages:
```
✓ Extracted thought_signature from extra_content
  - Shows signature was found in client's request

Preserved thought_signature in extra_content
  - Shows signature was added to response for client
```

### ⚠ Warning Messages:
```
No extra_content in tool call
  - Client didn't send extra_content (expected on first call)

thought_signature field not found or wrong type
  - extra_content exists but signature is missing/wrong format

google field not found or wrong type
  - extra_content exists but google field is missing/wrong format
```

## Example Debug Session

```bash
# Start with debug logging
./omniproxy --debug

# In another terminal, make requests
# Watch server.log for detailed conversion logs

# View logs in real-time
tail -f server.log
```

## Analyzing the Logs

Look for this sequence:

1. **First Request** (Client → Gemini):
   - Should show: "No extra_content in tool call" (normal, first call)

2. **First Response** (Gemini → Client):
   - Should show: "Preserved thought_signature in extra_content"
   - Check the response JSON has `extra_content.google.thought_signature`

3. **Second Request** (Client → Gemini with tool response):
   - Should show: "✓ Extracted thought_signature from extra_content"
   - Should show: Part with "has_thought_signature": true

4. **If Error Occurs**:
   - Check if step 3 shows "No extra_content in tool call"
   - This means client didn't preserve the extra_content field

## Troubleshooting

### Error: "function call missing a thought_signature"

Check the logs for:
1. Did we send `extra_content` to client? (Step 2)
2. Did client send `extra_content` back? (Step 3)
3. Did we extract and add it to the Gemini Part? (Step 3)

### Client Not Preserving extra_content

If logs show we sent `extra_content` but didn't receive it back:
- The client library may not support preserving unknown JSON fields
- This is common with strongly-typed languages (Go, Java, C#)
- Consider using a dynamic-typed client (JavaScript, Python) or implementing server-side storage

## Log File Management

The `server.log` file will grow over time. To manage it:

```bash
# Rotate the log
mv server.log server.log.old

# Or clear it
> serveog

# Or use logrotate (Linux)
```

## Disabling Debug Logging

Simply restart without the `--debug` flag or `DEBUG` environment variable:

```bash
./omniproxy
```
