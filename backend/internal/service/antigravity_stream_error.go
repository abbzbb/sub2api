package service

import (
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
)

// An error event is terminal even though the upstream HTTP status is 200.
// Keep account/project details out of both client events and returned errors.
type antigravityStreamError struct {
	status int
	body   []byte
}

func (e *antigravityStreamError) Error() string {
	return fmt.Sprintf("Antigravity upstream error event (status %d)", e.status)
}

func (e *antigravityStreamError) failover() *UpstreamFailoverError {
	return &UpstreamFailoverError{
		StatusCode:             e.status,
		ResponseBody:           e.body,
		RetryableOnSameAccount: e.status >= 500,
	}
}

func (e *antigravityStreamError) markFailed(c *gin.Context) {
	setOpsUpstreamError(c, e.status, e.Error(), "")
	MarkOpsStreamFailure(c, geminiSignalOpsErrorType(e.status), "UPSTREAM_ERROR", e.Error(), e.status)
}

func detectAntigravityStreamError(line string) *antigravityStreamError {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return nil
	}
	payload := []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
	signal, ok := detectGeminiResponseSignal(payload)
	if !ok || signal.Kind != geminiSignalError {
		return nil
	}
	return &antigravityStreamError{
		status: signal.Status,
		body:   buildAntigravityClientErrorBody(signal.Status, payload),
	}
}
