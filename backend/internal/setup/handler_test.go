package setup

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func setupTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterRoutes(r)
	return r
}

func withNeedsSetup(t *testing.T) {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	original, wasSet := os.LookupEnv("SKIP_SETUP")
	if err := os.Unsetenv("SKIP_SETUP"); err != nil {
		t.Fatalf("Unsetenv(SKIP_SETUP): %v", err)
	}
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv("SKIP_SETUP", original)
			return
		}
		_ = os.Unsetenv("SKIP_SETUP")
	})
}

func TestSetupGuardBlocksAfterInstall(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	if err := os.WriteFile(filepath.Join(dataDir, InstallLockFile), nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	r := setupTestRouter(t)
	for _, path := range []string{"/setup/test-db", "/setup/test-redis"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(`{}`))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s status = %d, want 403 after setup", path, w.Code)
		}
	}
}

func TestTestDatabaseRejectsBlockedHosts(t *testing.T) {
	withNeedsSetup(t)
	r := setupTestRouter(t)

	body := map[string]any{
		"host":     "169.254.169.254",
		"port":     5432,
		"user":     "sub2api",
		"password": "secret",
		"dbname":   "sub2api",
		"sslmode":  "disable",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/setup/test-db", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Host is not allowed") {
		t.Fatalf("body = %s, want host rejection", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "169.254") {
		t.Fatalf("response leaked probe target: %s", w.Body.String())
	}
}

func TestTestRedisRejectsPrivateIPLiteral(t *testing.T) {
	withNeedsSetup(t)
	r := setupTestRouter(t)

	body := map[string]any{
		"host": "10.0.0.5",
		"port": 6379,
		"db":   0,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/setup/test-redis", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "10.0.0.5") {
		t.Fatalf("response leaked probe target: %s", w.Body.String())
	}
	if strings.Contains(strings.ToLower(w.Body.String()), "connection refused") {
		t.Fatalf("response leaked connection error: %s", w.Body.String())
	}
}

func TestTestDatabaseGenericConnectionError(t *testing.T) {
	withNeedsSetup(t)
	r := setupTestRouter(t)

	body := map[string]any{
		"host":     "127.0.0.1",
		"port":     1,
		"user":     "sub2api",
		"password": "secret",
		"dbname":   "sub2api",
		"sslmode":  "disable",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/setup/test-db", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Connection failed") {
		t.Fatalf("body = %s, want generic connection failure", w.Body.String())
	}
	lower := strings.ToLower(w.Body.String())
	if strings.Contains(lower, "connection refused") || strings.Contains(lower, "dial tcp") {
		t.Fatalf("response leaked backend error: %s", w.Body.String())
	}
}
