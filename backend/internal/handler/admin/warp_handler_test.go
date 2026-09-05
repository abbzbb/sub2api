package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestWriteWarpSyncResultOmitsProxyPasswordAndUsesGroupID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/warp/sync", nil)

	secret := "warp-proxy-secret"
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	result := &service.WarpSyncResult{
		CreatedProxies: []service.Proxy{{
			ID:        11,
			Name:      "warp-1",
			Password:  secret,
			Status:    "active",
			CreatedAt: now,
			UpdatedAt: now,
		}},
		UpdatedProxies: []service.Proxy{{
			ID:       12,
			Name:     "warp-2",
			Password: secret,
			Status:   "active",
		}},
		DeletedProxies: []service.Proxy{{
			ID:       13,
			Name:     "warp-old",
			Password: secret,
			Status:   "inactive",
		}},
		Group: &service.ProxyGroupWithProxies{
			ProxyGroup: service.ProxyGroup{ID: 9, Name: "warp-pool", Status: "active"},
			Proxies: []service.Proxy{{
				ID:       11,
				Name:     "warp-1",
				Password: secret,
				Status:   "active",
			}},
			ProxyCount: 1,
		},
		MemberIDs: []int64{11},
	}

	writeWarpSyncResult(c, result)
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	require.NotContains(t, body, secret)
	require.NotContains(t, body, `"password"`)
	require.Contains(t, body, `"password_set"`)

	var envelope struct {
		Data struct {
			Group *struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"group"`
			CreatedProxies []map[string]any `json:"created_proxies"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	require.NotNil(t, envelope.Data.Group)
	require.Equal(t, int64(9), envelope.Data.Group.ID)
	require.Equal(t, "warp-pool", envelope.Data.Group.Name)
	require.NotEmpty(t, envelope.Data.CreatedProxies)
	_, hasPassword := envelope.Data.CreatedProxies[0]["password"]
	require.False(t, hasPassword)
	require.Equal(t, true, envelope.Data.CreatedProxies[0]["password_set"])
	require.False(t, strings.Contains(strings.ToLower(body), `"id":`) && envelope.Data.Group.ID == 0)
}
