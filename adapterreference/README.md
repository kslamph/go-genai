# Adapter Reference

This folder contains adapter code that was previously used for OpenAI-to-native protocol conversion. These files are kept as reference for future native protocol implementation.

## Files

- `gemini_adapter.go` - OpenAI to Gemini protocol conversion (ToGeminiRequest, FromGeminiResponse, etc.)
- `kiro_adapter.go` - OpenAI to Kiro protocol conversion (ToKiroRequest, etc.)

## Purpose

These adapters show:
- Native API request/response structures for each provider
- How to convert between OpenAI format and provider-specific formats
- Tool/function calling implementations
- Streaming response handling

## Usage

These files are NOT used in the current codebase. They are provided as reference for implementing native protocol access in the future.