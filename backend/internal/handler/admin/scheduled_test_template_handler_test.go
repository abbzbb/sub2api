package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type templateDeleteRepo struct {
	service.QualityRuleTemplateRepository
	err         error
	deleted     int
	deletePlans bool
	id          int64
}

func (r *templateDeleteRepo) Delete(_ context.Context, id int64, deletePlans bool) (int, error) {
	r.id, r.deletePlans = id, deletePlans
	return r.deleted, r.err
}

func TestQualityTemplateDeleteHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name        string
		deletePlans bool
		err         error
		status      int
		reason      string
	}{
		{"scope conflict", true, service.QualityScopeConflict("restore isolation before deleting"), http.StatusConflict, service.QualityScopeConflictReason},
		{"missing", true, service.ErrQualityTemplateNotFound, http.StatusNotFound, ""},
		{"database failure", true, errors.New("database failure"), http.StatusInternalServerError, ""},
		{"delete with rules", true, nil, http.StatusOK, ""},
		{"keep rules", false, nil, http.StatusOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &templateDeleteRepo{err: tc.err}
			if tc.deletePlans && tc.err == nil {
				repo.deleted = 2
			}
			h := NewScheduledTestHandler(service.ProvideScheduledTestService(nil, nil, repo, nil))
			router := gin.New()
			router.DELETE("/templates/:id", h.DeleteQualityTemplate)
			path := "/templates/7"
			if tc.deletePlans {
				path += "?delete_plans=true"
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, path, nil))
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.Equal(t, int64(7), repo.id)
			require.Equal(t, tc.deletePlans, repo.deletePlans)
			var body struct {
				Code    int    `json:"code"`
				Reason  string `json:"reason"`
				Deleted int    `json:"deleted"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			require.Equal(t, tc.reason, body.Reason)
			if tc.err != nil {
				require.Equal(t, tc.status, body.Code)
			} else {
				require.Equal(t, repo.deleted, body.Deleted)
			}
		})
	}
}
