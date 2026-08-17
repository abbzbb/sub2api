package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type stubReadyChecker struct {
	err error
}

func (s stubReadyChecker) Ping(context.Context) error {
	return s.err
}

func TestHealthLiveKeepsLegacyContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterCommonRoutes(r, HealthDependencies{})

	for _, path := range []string{"/health", "/health/live"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, w.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s json: %v", path, err)
		}
		if body["status"] != "ok" {
			t.Fatalf("%s body = %s, want status ok", path, w.Body.String())
		}
	}
}

func TestHealthReadyRequiresDBAndRedis(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("both healthy", func(t *testing.T) {
		r := gin.New()
		RegisterCommonRoutes(r, HealthDependencies{
			DB:    stubReadyChecker{},
			Redis: stubReadyChecker{},
		})
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
		if !jsonContains(w.Body.Bytes(), "database", "ok") || !jsonContains(w.Body.Bytes(), "redis", "ok") {
			t.Fatalf("body = %s", w.Body.String())
		}
	})

	t.Run("database down", func(t *testing.T) {
		r := gin.New()
		RegisterCommonRoutes(r, HealthDependencies{
			DB:    stubReadyChecker{err: errors.New("db down")},
			Redis: stubReadyChecker{},
		})
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
		if jsonContains(w.Body.Bytes(), "database", "ok") {
			t.Fatalf("database should be unavailable: %s", w.Body.String())
		}
		if stringContains(w.Body.String(), "db down") {
			t.Fatalf("ready response leaked probe error: %s", w.Body.String())
		}
	})

	t.Run("missing probes fail closed", func(t *testing.T) {
		r := gin.New()
		RegisterCommonRoutes(r, HealthDependencies{})
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
	})
}

func jsonContains(raw []byte, key, value string) bool {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return false
	}
	checks, _ := body["checks"].(map[string]any)
	if checks == nil {
		return false
	}
	got, _ := checks[key].(string)
	return got == value
}

func stringContains(s, sub string) bool {
	return len(sub) > 0 && (s == sub || (len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()))
}
