package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPaymentOrdersRefundEligibleProvidersRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	var matchedPath, matchedHandler, orderID string
	// Capture the production route at the auth boundary without invoking storage.
	auth := middleware.JWTAuthMiddleware(func(c *gin.Context) {
		matchedPath, matchedHandler, orderID = c.FullPath(), c.HandlerName(), c.Param("id")
		c.AbortWithStatus(http.StatusUnauthorized)
	})
	noop := func(c *gin.Context) { c.Next() }
	RegisterPaymentRoutes(router.Group("/api/v1"), &handler.PaymentHandler{},
		&handler.PaymentWebhookHandler{}, &admin.PaymentHandler{}, auth,
		middleware.AdminAuthMiddleware(noop), middleware.AuditLogMiddleware(noop), nil, nil)

	for _, tc := range []struct {
		path, route, handler, id string
	}{
		{"/api/v1/payment/orders/refund-eligible-providers", "/api/v1/payment/orders/refund-eligible-providers", "GetRefundEligibleProviders", ""},
		{"/api/v1/payment/orders/42", "/api/v1/payment/orders/:id", "GetOrder", "42"},
		{"/api/v1/payment/orders/my", "/api/v1/payment/orders/my", "GetMyOrders", ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			matchedPath, matchedHandler, orderID = "", "", ""
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			require.Equal(t, http.StatusUnauthorized, rec.Code)
			require.Equal(t, tc.route, matchedPath)
			require.Contains(t, matchedHandler, ".(*PaymentHandler)."+tc.handler+"-fm")
			require.Equal(t, tc.id, orderID)
		})
	}
}
