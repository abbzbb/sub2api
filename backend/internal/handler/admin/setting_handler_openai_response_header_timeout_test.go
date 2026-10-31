package admin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIResponseHeaderTimeoutAdminValidationAndDisable(t *testing.T) {
	h, _ := newStepUpSwitchTestHandler(t, map[string]string{})
	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/openai-response-header-timeout", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.UpdateOpenAIResponseHeaderTimeoutSettings(c)
		return rec
	}
	for _, body := range []string{
		`{}`, `null`, `{"seconds":20}`, `{"enabled":true}`, `{"enabled":null,"seconds":20}`,
		`{"enabled":true,"seconds":0}`, `{"enabled":false,"seconds":0}`,
		`{"enabled":true,"seconds":-1}`, `{"enabled":true,"seconds":601}`,
		`{"enabled":true,"seconds":1.5}`, `{"enabled":true,"seconds":999999999999999999999999}`,
	} {
		rec := put(body)
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
	}
	for _, seconds := range []int{1, 600} {
		rec := put(fmt.Sprintf(`{"enabled":true,"seconds":%d}`, seconds))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.True(t, gjson.GetBytes(rec.Body.Bytes(), "data.enabled").Bool())
		require.EqualValues(t, seconds, gjson.GetBytes(rec.Body.Bytes(), "data.seconds").Int())
	}
	rec := put(`{"enabled":false,"seconds":20}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.False(t, gjson.GetBytes(rec.Body.Bytes(), "data.enabled").Bool())
	_, enabled := h.settingService.GetOpenAIResponseHeaderTimeoutCached(t.Context())
	require.False(t, enabled)
}
