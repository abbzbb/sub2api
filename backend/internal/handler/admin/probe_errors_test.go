package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func TestAllowedTestEmailRecipients(t *testing.T) {
	t.Parallel()

	got := allowedTestEmailRecipients(" Admin@Example.com ", &service.SMTPConfig{
		From:     "noreply@example.com",
		Username: "smtp-user@example.com",
	})
	want := []string{"admin@example.com", "noreply@example.com", "smtp-user@example.com"}
	if len(got) != len(want) {
		t.Fatalf("allowed recipients = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("allowed recipients = %#v, want %#v", got, want)
		}
	}

	if !isAllowedTestEmailRecipient("admin@example.com", got) {
		t.Fatal("expected current admin email to be allowed")
	}
	if isAllowedTestEmailRecipient("outsider@evil.test", got) {
		t.Fatal("expected unrelated recipient to be rejected")
	}
	if isAllowedTestEmailRecipient("", got) {
		t.Fatal("expected empty recipient to be rejected")
	}
}

func TestAllowedTestEmailRecipientsIgnoresNonEmailUsername(t *testing.T) {
	t.Parallel()

	got := allowedTestEmailRecipients("", &service.SMTPConfig{
		From:     "alerts@example.com",
		Username: "apikey",
	})
	if len(got) != 1 || got[0] != "alerts@example.com" {
		t.Fatalf("allowed recipients = %#v", got)
	}
}

func TestAdminProbeFailedDoesNotEchoUpstreamError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/test-smtp", nil)

	adminProbeFailed(c, http.StatusBadRequest, reasonSMTPProbeFailed, genericSMTPProbeMessage(), errors.New("dial tcp 169.254.169.254:80: i/o timeout"))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	var env response.Response
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if env.Message != genericSMTPProbeMessage() {
		t.Fatalf("message = %q", env.Message)
	}
	if env.Reason != reasonSMTPProbeFailed {
		t.Fatalf("reason = %q", env.Reason)
	}
	body := w.Body.String()
	for _, leaked := range []string{"169.254.169.254", "i/o timeout", "dial tcp"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("response leaked probe details %q: %s", leaked, body)
		}
	}
}
