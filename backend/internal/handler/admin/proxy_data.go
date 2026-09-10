package admin

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ExportData exports proxy-only data for migration.
func (h *ProxyHandler) ExportData(c *gin.Context) {
	ctx := c.Request.Context()

	selectedIDs, err := parseProxyIDs(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	var proxies []service.Proxy
	if len(selectedIDs) > 0 {
		proxies, err = h.getProxiesByIDs(ctx, selectedIDs)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		proxies, err = expandBackupProxyChain(ctx, h.adminService, proxies)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
	} else {
		protocol := c.Query("protocol")
		status := c.Query("status")
		search := strings.TrimSpace(c.Query("search"))
		sortBy := c.DefaultQuery("sort_by", "id")
		sortOrder := c.DefaultQuery("sort_order", "desc")
		if len(search) > 100 {
			search = search[:100]
		}

		proxies, err = h.listProxiesFiltered(ctx, protocol, status, search, sortBy, sortOrder)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
	}

	dataProxies := dataProxiesFromService(proxies)

	payload := DataPayload{
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Proxies:    dataProxies,
		Accounts:   []DataAccount{},
	}

	response.Success(c, payload)
}

// ImportData imports proxy-only data for migration.
func (h *ProxyHandler) ImportData(c *gin.Context) {
	type ProxyImportRequest struct {
		Data DataPayload `json:"data"`
	}

	var req ProxyImportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	if err := validateDataHeader(req.Data); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	ctx := c.Request.Context()
	result := DataImportResult{}

	existingProxies, err := h.listProxiesFiltered(ctx, "", "", "", "id", "desc")
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	_, latencyProbeIDs := importDataProxies(ctx, h.adminService, existingProxies, req.Data.Proxies, &result)

	if len(latencyProbeIDs) > 0 {
		ids := append([]int64(nil), latencyProbeIDs...)
		go func() {
			for _, id := range ids {
				_, _ = h.adminService.TestProxy(context.Background(), id)
			}
		}()
	}

	response.Success(c, result)
}

func (h *ProxyHandler) getProxiesByIDs(ctx context.Context, ids []int64) ([]service.Proxy, error) {
	if len(ids) == 0 {
		return []service.Proxy{}, nil
	}
	return h.adminService.GetProxiesByIDs(ctx, ids)
}

func parseProxyIDs(c *gin.Context) ([]int64, error) {
	values := c.QueryArray("ids")
	if len(values) == 0 {
		raw := strings.TrimSpace(c.Query("ids"))
		if raw != "" {
			values = []string{raw}
		}
	}
	if len(values) == 0 {
		return nil, nil
	}

	ids := make([]int64, 0, len(values))
	for _, item := range values {
		for _, part := range strings.Split(item, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, err := strconv.ParseInt(part, 10, 64)
			if err != nil || id <= 0 {
				return nil, fmt.Errorf("invalid proxy id: %s", part)
			}
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (h *ProxyHandler) listProxiesFiltered(ctx context.Context, protocol, status, search, sortBy, sortOrder string) ([]service.Proxy, error) {
	page := 1
	pageSize := dataPageCap
	var out []service.Proxy
	sortBy = strings.TrimSpace(sortBy)
	useAccountCountSort := strings.EqualFold(sortBy, "account_count")
	for {
		if useAccountCountSort {
			items, total, err := h.adminService.ListProxiesWithAccountCount(ctx, page, pageSize, protocol, status, search, sortBy, sortOrder)
			if err != nil {
				return nil, err
			}
			for i := range items {
				out = append(out, items[i].Proxy)
			}
			if len(out) >= int(total) || len(items) == 0 {
				break
			}
		} else {
			items, total, err := h.adminService.ListProxies(ctx, page, pageSize, protocol, status, search, sortBy, sortOrder)
			if err != nil {
				return nil, err
			}
			out = append(out, items...)
			if len(out) >= int(total) || len(items) == 0 {
				break
			}
		}
		page++
	}
	return out, nil
}
