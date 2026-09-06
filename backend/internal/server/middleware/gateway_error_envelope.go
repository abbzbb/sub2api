package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// isGatewayAPIPath reports whether path is a public gateway surface whose clients
// expect Anthropic/OpenAI/Google error envelopes rather than panel JSON.
// Panel routes under /api/ stay on the admin/user envelope.
func isGatewayAPIPath(path string) bool {
	path = strings.ToLower(strings.TrimSpace(path))
	if path == "" || strings.HasPrefix(path, "/api/") {
		return false
	}
	switch {
	case path == "/v1", strings.HasPrefix(path, "/v1/"):
		return true
	case strings.HasPrefix(path, "/v1beta"),
		strings.HasPrefix(path, "/openai/"),
		strings.HasPrefix(path, "/antigravity"),
		strings.HasPrefix(path, "/backend-api/codex"):
		return true
	case strings.HasPrefix(path, "/responses"),
		strings.HasPrefix(path, "/chat/completions"),
		strings.HasPrefix(path, "/messages"),
		strings.HasPrefix(path, "/embeddings"),
		strings.HasPrefix(path, "/images"),
		strings.HasPrefix(path, "/videos"),
		strings.HasPrefix(path, "/models"),
		strings.HasPrefix(path, "/tts"),
		strings.HasPrefix(path, "/stt"),
		strings.HasPrefix(path, "/realtime"),
		strings.HasPrefix(path, "/web_search"),
		strings.HasPrefix(path, "/x_search"),
		strings.HasPrefix(path, "/custom-voices"),
		strings.HasPrefix(path, "/alpha/"):
		return true
	default:
		return false
	}
}

// OpenAIErrorWriter 按 OpenAI API 规范输出错误
func OpenAIErrorWriter(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"type":    openAIErrorTypeForStatus(status),
			"message": message,
		},
	})
}

// GatewayProtocolErrorWriter selects Completions/Responses/Anthropic/Gemini envelopes.
func GatewayProtocolErrorWriter(c *gin.Context, status int, message string) {
	switch gatewayErrorProtocol(c) {
	case "google":
		GoogleErrorWriter(c, status, message)
	case "openai":
		OpenAIErrorWriter(c, status, message)
	default:
		AnthropicErrorWriter(c, status, message)
	}
}

func gatewayErrorProtocol(c *gin.Context) string {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return "anthropic"
	}
	_, protocol := ingressRejectRoute(c.Request.URL.Path)
	if protocol == "openai" || protocol == "google" || protocol == "anthropic" {
		return protocol
	}
	path := strings.ToLower(c.Request.URL.Path)
	switch {
	case strings.Contains(path, "/v1beta"), strings.Contains(path, "/antigravity/v1beta"):
		return "google"
	case strings.Contains(path, "/messages"):
		return "anthropic"
	default:
		return "openai"
	}
}

func abortWithGatewayProtocolError(c *gin.Context, statusCode int, code, message string) {
	switch gatewayErrorProtocol(c) {
	case "google":
		GoogleErrorWriter(c, statusCode, message)
	case "openai":
		if code == "API_KEY_QUOTA_EXHAUSTED" {
			writeOpenAIQuotaError(c, statusCode, message)
		} else {
			writeOpenAIErrorWithCode(c, statusCode, code, message)
		}
	default:
		writeAnthropicErrorWithCode(c, statusCode, code, message)
	}
	c.Abort()
}

func writeOpenAIErrorWithCode(c *gin.Context, status int, code, message string) {
	errObj := gin.H{
		"type":    openAIErrorTypeForStatus(status),
		"message": message,
	}
	if code != "" {
		errObj["code"] = code
	}
	c.JSON(status, gin.H{"error": errObj})
}

func writeOpenAIQuotaError(c *gin.Context, statusCode int, message string) {
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"message": message,
			"type":    "insufficient_quota",
			"param":   nil,
			"code":    "insufficient_quota",
		},
	})
}

func writeAnthropicErrorWithCode(c *gin.Context, status int, code, message string) {
	errObj := gin.H{
		"type":    anthropicErrorTypeForStatus(status),
		"message": message,
	}
	if code != "" {
		errObj["code"] = code
	}
	c.JSON(status, gin.H{
		"type":  "error",
		"error": errObj,
	})
}

func openAIErrorTypeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "invalid_request_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "invalid_request_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusServiceUnavailable:
		return "api_error"
	default:
		if status >= 500 {
			return "api_error"
		}
		return "invalid_request_error"
	}
}

func anthropicErrorTypeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	default:
		if status >= 500 {
			return "api_error"
		}
		return "api_error"
	}
}
