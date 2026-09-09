package middleware

import (
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"

	"github.com/gin-gonic/gin"
)

// IngressRejectReason identifies expected gateway admission failures that must
// not be treated as operational request errors.
type IngressRejectReason string

const (
	IngressRejectQueryAPIKeyDeprecated  IngressRejectReason = "query_api_key_deprecated"
	IngressRejectAPIKeyRequired         IngressRejectReason = "api_key_required"
	IngressRejectInvalidAPIKey          IngressRejectReason = "invalid_api_key"
	IngressRejectAPIKeyDisabled         IngressRejectReason = "api_key_disabled"
	IngressRejectIPRestricted           IngressRejectReason = "ip_restricted"
	IngressRejectUserInactive           IngressRejectReason = "user_inactive"
	IngressRejectGroupDeleted           IngressRejectReason = "group_deleted"
	IngressRejectGroupDisabled          IngressRejectReason = "group_disabled"
	IngressRejectGroupNotAllowed        IngressRejectReason = "group_not_allowed"
	IngressRejectGroupUnassigned        IngressRejectReason = "group_unassigned"
	IngressRejectInvalidAuthRateLimited IngressRejectReason = "invalid_auth_rate_limited"
	IngressRejectAPIKeyAuthOverloaded   IngressRejectReason = "api_key_auth_overloaded"
	IngressRejectModelNotAllowed        IngressRejectReason = "model_not_allowed"
)

const ingressRejectReasonContextKey = "ingress_reject_reason"

type IngressRejectRecorder interface {
	RecordIngressReject(reason, routeFamily, protocol, clientIP string, userID, apiKeyID int64)
}

func invalidAuthClientKey(c *gin.Context) string {
	return normalizeIngressRejectIP(SecurityClientIP(c))
}

func rejectInvalidAuthAbuse(c *gin.Context, apiKeyService interface {
	CheckInvalidAuthAbuse(string) (time.Duration, bool)
}) bool {
	if c == nil || apiKeyService == nil {
		return false
	}
	retry, blocked := apiKeyService.CheckInvalidAuthAbuse(invalidAuthClientKey(c))
	if !blocked {
		return false
	}
	retrySeconds := int(math.Ceil(retry.Seconds()))
	if retrySeconds < 1 {
		retrySeconds = 1
	}
	c.Header("Retry-After", strconv.Itoa(retrySeconds))
	MarkIngressRejected(c, IngressRejectInvalidAuthRateLimited)
	return true
}

func recordInvalidAuthFailure(c *gin.Context, apiKeyService interface {
	RecordInvalidAuthFailure(string)
}) {
	if c == nil || apiKeyService == nil {
		return
	}
	apiKeyService.RecordInvalidAuthFailure(invalidAuthClientKey(c))
}

type ingressRejectRecorderHolder struct{ recorder IngressRejectRecorder }

var activeIngressRejectRecorder atomic.Pointer[ingressRejectRecorderHolder]

func SetIngressRejectRecorder(recorder IngressRejectRecorder) {
	if recorder == nil {
		activeIngressRejectRecorder.Store(nil)
		return
	}
	activeIngressRejectRecorder.Store(&ingressRejectRecorderHolder{recorder: recorder})
}

// MarkIngressRejected marks a request as rejected before gateway admission.
func MarkIngressRejected(c *gin.Context, reason IngressRejectReason) {
	if c == nil || reason == "" {
		return
	}
	c.Set(ingressRejectReasonContextKey, reason)
}

// GetIngressRejectReason returns the admission rejection reason, if any.
func GetIngressRejectReason(c *gin.Context) (IngressRejectReason, bool) {
	if c == nil {
		return "", false
	}
	value, exists := c.Get(ingressRejectReasonContextKey)
	if !exists {
		return "", false
	}
	reason, ok := value.(IngressRejectReason)
	return reason, ok && reason != ""
}

func recordIngressReject(c *gin.Context, reason IngressRejectReason) {
	holder := activeIngressRejectRecorder.Load()
	if holder == nil || holder.recorder == nil || c == nil || c.Request == nil {
		return
	}
	routeFamily, protocol := ingressRejectRoute(c.Request.URL.Path)
	clientIP := normalizeIngressRejectIP(SecurityClientIP(c))
	var userID, apiKeyID int64
	if apiKey, ok := GetAPIKeyFromContext(c); ok && apiKey != nil {
		apiKeyID = apiKey.ID
		if apiKey.User != nil {
			userID = apiKey.User.ID
		}
	} else if apiKey, ok := GetOpsFallbackAPIKey(c); ok && apiKey != nil {
		apiKeyID = apiKey.ID
		if apiKey.User != nil {
			userID = apiKey.User.ID
		}
	}
	holder.recorder.RecordIngressReject(string(reason), routeFamily, protocol, clientIP, userID, apiKeyID)
}

func normalizeIngressRejectIP(raw string) string {
	// Shared /64 collapse with connection-risk signals (pkg/ip).
	// Invalid still maps to 0.0.0.0 for abuse counters (historical behaviour).
	if normalized := ip.NormalizeClientIPForSecurity(raw); normalized != "" {
		return normalized
	}
	return "0.0.0.0"
}

func ingressRejectRoute(path string) (string, string) {
	path = strings.ToLower(strings.TrimSpace(path))
	switch {
	case hasIngressRouteNamespace(path, "/antigravity/v1beta"):
		return "antigravity", "google"
	case hasIngressRouteNamespace(path, "/v1beta"):
		return "gemini", "google"
	case hasIngressRouteNamespace(path, "/backend-api/codex"):
		return "codex", "openai"
	case hasIngressRouteNamespace(path, "/antigravity"):
		return "antigravity", "anthropic"
	case path == "/v1/usage":
		return "other", "anthropic"
	case hasIngressRouteNamespace(path, "/v1/messages"), hasIngressRouteNamespace(path, "/messages"):
		return "messages", "anthropic"
	case hasIngressRouteNamespace(path, "/v1/responses"), hasIngressRouteNamespace(path, "/responses"):
		return "responses", "openai"
	case hasIngressRouteNamespace(path, "/v1/chat/completions"), hasIngressRouteNamespace(path, "/chat/completions"):
		return "chat_completions", "openai"
	case hasIngressRouteNamespace(path, "/v1/images"), hasIngressRouteNamespace(path, "/images"):
		return "images", "openai"
	case hasIngressRouteNamespace(path, "/v1/videos"), hasIngressRouteNamespace(path, "/videos"):
		return "videos", "openai"
	case hasIngressRouteNamespace(path, "/v1/embeddings"), hasIngressRouteNamespace(path, "/embeddings"):
		return "embeddings", "openai"
	case hasIngressRouteNamespace(path, "/v1/models"), hasIngressRouteNamespace(path, "/models"):
		return "models", "openai"
	default:
		return "other", "gateway"
	}
}

func hasIngressRouteNamespace(path, namespace string) bool {
	return path == namespace || strings.HasPrefix(path, namespace+"/")
}
