package admin

import (
	"log/slog"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	reasonSMTPHostRequired             = "SMTP_HOST_REQUIRED"
	reasonSMTPProbeFailed              = "SMTP_PROBE_FAILED"
	reasonTestEmailRecipientNotAllowed = "TEST_EMAIL_RECIPIENT_NOT_ALLOWED"
	reasonTestEmailSendFailed          = "TEST_EMAIL_SEND_FAILED"
	reasonS3ProbeFailed                = "S3_PROBE_FAILED"
	reasonS3ConfigIncomplete           = "S3_CONFIG_INCOMPLETE"
	reasonUpdateCheckFailed            = "UPDATE_CHECK_FAILED"
	reasonRollbackListFailed           = "ROLLBACK_LIST_FAILED"
)

func adminProbeFailed(c *gin.Context, status int, reason, message string, err error) {
	if err != nil {
		path := ""
		if c != nil && c.Request != nil {
			path = c.FullPath()
			if path == "" {
				path = c.Request.URL.Path
			}
		}
		slog.Warn("admin probe failed", "reason", reason, "path", path, "err", err)
	}
	response.ErrorWithDetails(c, status, message, reason, nil)
}

func normalizeEmailAddr(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func allowedTestEmailRecipients(adminEmail string, smtp *service.SMTPConfig) []string {
	seen := make(map[string]struct{}, 3)
	out := make([]string, 0, 3)
	add := func(value string) {
		email := normalizeEmailAddr(value)
		if email == "" {
			return
		}
		if _, ok := seen[email]; ok {
			return
		}
		seen[email] = struct{}{}
		out = append(out, email)
	}
	add(adminEmail)
	if smtp != nil {
		add(smtp.From)
		if strings.Contains(smtp.Username, "@") {
			add(smtp.Username)
		}
	}
	return out
}

func isAllowedTestEmailRecipient(recipient string, allowed []string) bool {
	target := normalizeEmailAddr(recipient)
	if target == "" {
		return false
	}
	for _, email := range allowed {
		if target == email {
			return true
		}
	}
	return false
}

func genericSMTPProbeMessage() string {
	return "SMTP connection test failed"
}

func genericTestEmailSendMessage() string {
	return "Failed to send test email"
}

func genericS3ProbeMessage() string {
	return "S3 connection test failed"
}

func genericUpdateCheckMessage() string {
	return "Update check failed"
}

func genericRollbackListMessage() string {
	return "Failed to list rollback versions"
}
