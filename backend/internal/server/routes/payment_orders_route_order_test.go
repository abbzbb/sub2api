package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// Mirrors payment orders GET registration: static paths before /:id.
func TestPaymentOrdersRefundEligibleProvidersNotSwallowedByID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	orders := router.Group("/orders")
	{
		orders.GET("/my", func(c *gin.Context) { c.String(http.StatusOK, "my") })
		orders.GET("/refund-eligible-providers", func(c *gin.Context) { c.String(http.StatusOK, "providers") })
		orders.GET("/:id", func(c *gin.Context) { c.String(http.StatusOK, "id:"+c.Param("id")) })
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/orders/refund-eligible-providers", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "providers" {
		t.Fatalf("static path swallowed by :id: code=%d body=%q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/orders/42", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "id:42" {
		t.Fatalf("param route broken: code=%d body=%q", rec.Code, rec.Body.String())
	}
}
