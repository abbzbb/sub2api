package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
)

func extractMaxBytesError(err error) (*http.MaxBytesError, bool) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return maxErr, true
	}
	return nil, false
}

func formatBodyLimit(limit int64) string {
	const mb = 1024 * 1024
	if limit >= mb {
		return fmt.Sprintf("%dMB", limit/mb)
	}
	return fmt.Sprintf("%dB", limit)
}

func buildBodyTooLargeMessage(limit int64) string {
	return fmt.Sprintf("Request body too large, limit is %s", formatBodyLimit(limit))
}

// buildBodyReadErrorMessage maps body-read failures to actionable client messages
// instead of the opaque "Failed to read request body" (#4056).
func buildBodyReadErrorMessage(err error) string {
	if err == nil {
		return "Failed to read request body"
	}
	if maxErr, ok := extractMaxBytesError(err); ok {
		return buildBodyTooLargeMessage(maxErr.Limit)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "Client disconnected while reading request body"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return "Incomplete request body"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "Timed out while reading request body"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "request body too large"), strings.Contains(msg, "http: request body too large"):
		return "Request body too large"
	case strings.Contains(msg, "client disconnected"), strings.Contains(msg, "connection reset"):
		return "Client disconnected while reading request body"
	case strings.Contains(msg, "decode content-encoding"), strings.Contains(msg, "content-encoding"):
		return "Failed to decode request body Content-Encoding"
	case strings.Contains(msg, "unsupported content-encoding"):
		return "Unsupported request Content-Encoding"
	default:
		return "Failed to read request body"
	}
}

func readLenientJSONRequestBodyWithPrealloc(req *http.Request, cfg *config.Config) ([]byte, error) {
	return pkghttputil.ReadLenientJSONRequestBodyWithPrealloc(req, gatewayMaxBodySize(cfg))
}

func gatewayMaxBodySize(cfg *config.Config) int64 {
	if cfg == nil {
		return 0
	}
	return cfg.Gateway.MaxBodySize
}
