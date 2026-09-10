package admin

import (
	"context"
	"fmt"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type dataProxyWriter interface {
	CreateProxy(ctx context.Context, input *service.CreateProxyInput) (*service.Proxy, error)
	UpdateProxy(ctx context.Context, id int64, input *service.UpdateProxyInput) (*service.Proxy, error)
}

type dataProxyByIDsLookup interface {
	GetProxiesByIDs(ctx context.Context, ids []int64) ([]service.Proxy, error)
}

type pendingProxyRel struct {
	id   int64
	key  string
	item DataProxy
}

func dataProxiesFromService(proxies []service.Proxy) []DataProxy {
	proxyNameByID := make(map[int64]string, len(proxies))
	for i := range proxies {
		proxyNameByID[proxies[i].ID] = proxies[i].Name
	}

	dataProxies := make([]DataProxy, 0, len(proxies))
	for i := range proxies {
		p := proxies[i]
		key := buildProxyKey(p.Protocol, p.Host, p.Port, p.Username, p.Password)

		var expiresAt *int64
		if p.ExpiresAt != nil {
			v := p.ExpiresAt.Unix()
			expiresAt = &v
		}
		var backupProxyName string
		if p.BackupProxyID != nil {
			backupProxyName = proxyNameByID[*p.BackupProxyID]
		}
		dataProxies = append(dataProxies, DataProxy{
			ProxyKey:        key,
			Name:            p.Name,
			Protocol:        p.Protocol,
			Host:            p.Host,
			Port:            p.Port,
			Username:        p.Username,
			Password:        p.Password,
			Status:          p.Status,
			ExpiresAt:       expiresAt,
			FallbackMode:    p.FallbackMode,
			BackupProxyName: backupProxyName,
			ExpiryWarnDays:  p.ExpiryWarnDays,
		})
	}
	return dataProxies
}

// expandBackupProxyChain 把选中代理的 BackupProxyID 链路一并纳入导出集合。
// 成环会停止扩展；引用的备用代理在库中不存在时返回明确错误，避免导出无法还原的数据。
func expandBackupProxyChain(ctx context.Context, lookup dataProxyByIDsLookup, selected []service.Proxy) ([]service.Proxy, error) {
	if len(selected) == 0 {
		return selected, nil
	}

	byID := make(map[int64]service.Proxy, len(selected))
	out := make([]service.Proxy, 0, len(selected))
	for i := range selected {
		p := selected[i]
		if _, ok := byID[p.ID]; ok {
			continue
		}
		byID[p.ID] = p
		out = append(out, p)
	}

	for {
		missing := make([]int64, 0)
		seenMissing := make(map[int64]struct{})
		referrerName := make(map[int64]string)
		for _, p := range byID {
			if p.BackupProxyID == nil {
				continue
			}
			bid := *p.BackupProxyID
			if bid <= 0 {
				continue
			}
			if _, ok := byID[bid]; ok {
				continue
			}
			if _, ok := seenMissing[bid]; ok {
				continue
			}
			seenMissing[bid] = struct{}{}
			missing = append(missing, bid)
			referrerName[bid] = p.Name
		}
		if len(missing) == 0 {
			return out, nil
		}

		fetched, err := lookup.GetProxiesByIDs(ctx, missing)
		if err != nil {
			return nil, err
		}
		found := make(map[int64]struct{}, len(fetched))
		for i := range fetched {
			fp := fetched[i]
			found[fp.ID] = struct{}{}
			if _, ok := byID[fp.ID]; ok {
				continue
			}
			byID[fp.ID] = fp
			out = append(out, fp)
		}
		for _, bid := range missing {
			if _, ok := found[bid]; ok {
				continue
			}
			return nil, infraerrors.BadRequest(
				"PROXY_BACKUP_EXPORT_INCOMPLETE",
				fmt.Sprintf("selected proxy %q references missing backup proxy id %d", referrerName[bid], bid),
			)
		}
	}
}

func importDataProxies(ctx context.Context, svc dataProxyWriter, existing []service.Proxy, items []DataProxy, result *DataImportResult) (map[string]int64, []int64) {
	proxyByKey := make(map[string]service.Proxy, len(existing)+len(items))
	proxyKeyToID := make(map[string]int64, len(existing)+len(items))
	proxyNameToID := make(map[string]int64, len(existing)+len(items))
	for i := range existing {
		p := existing[i]
		key := buildProxyKey(p.Protocol, p.Host, p.Port, p.Username, p.Password)
		proxyByKey[key] = p
		proxyKeyToID[key] = p.ID
		if p.Name != "" {
			proxyNameToID[p.Name] = p.ID
		}
	}

	pending := make([]pendingProxyRel, 0, len(items))
	latencyProbeIDs := make([]int64, 0, len(items))

	for i := range items {
		item := items[i]
		key := item.ProxyKey
		if key == "" {
			key = buildProxyKey(item.Protocol, item.Host, item.Port, item.Username, item.Password)
		}
		if err := validateDataProxy(item); err != nil {
			result.ProxyFailed++
			result.Errors = append(result.Errors, DataImportError{
				Kind:     "proxy",
				Name:     item.Name,
				ProxyKey: key,
				Message:  err.Error(),
			})
			continue
		}

		normalizedStatus := normalizeProxyStatus(item.Status)
		var expiresAt *time.Time
		if item.ExpiresAt != nil {
			t := time.Unix(*item.ExpiresAt, 0).UTC()
			expiresAt = &t
		}

		if existingProxy, ok := proxyByKey[key]; ok {
			result.ProxyReused++
			registerImportedProxyNames(proxyNameToID, item.Name, existingProxy.Name, existingProxy.ID)
			proxyKeyToID[key] = existingProxy.ID
			if normalizedStatus != "" && normalizedStatus != existingProxy.Status {
				if err := updateImportedProxyStatus(ctx, svc, existingProxy, item, normalizedStatus, expiresAt); err != nil {
					result.Errors = append(result.Errors, DataImportError{
						Kind:     "proxy",
						Name:     item.Name,
						ProxyKey: key,
						Message:  "update status failed: " + err.Error(),
					})
				}
			}
			latencyProbeIDs = append(latencyProbeIDs, existingProxy.ID)
			pending = append(pending, pendingProxyRel{id: existingProxy.ID, key: key, item: item})
			continue
		}

		createMode := item.FallbackMode
		if createMode == "" || createMode == service.FallbackModeProxy {
			createMode = service.FallbackModeNone
		}
		created, err := svc.CreateProxy(ctx, &service.CreateProxyInput{
			Name:           defaultProxyName(item.Name),
			Protocol:       item.Protocol,
			Host:           item.Host,
			Port:           item.Port,
			Username:       item.Username,
			Password:       item.Password,
			ExpiresAt:      expiresAt,
			FallbackMode:   createMode,
			BackupProxyID:  nil,
			ExpiryWarnDays: item.ExpiryWarnDays,
		})
		if err != nil {
			result.ProxyFailed++
			result.Errors = append(result.Errors, DataImportError{
				Kind:     "proxy",
				Name:     item.Name,
				ProxyKey: key,
				Message:  err.Error(),
			})
			continue
		}
		result.ProxyCreated++
		proxyByKey[key] = *created
		proxyKeyToID[key] = created.ID
		registerImportedProxyNames(proxyNameToID, item.Name, created.Name, created.ID)

		if normalizedStatus != "" && normalizedStatus != created.Status {
			if err := updateImportedProxyStatus(ctx, svc, *created, item, normalizedStatus, expiresAt); err != nil {
				result.Errors = append(result.Errors, DataImportError{
					Kind:     "proxy",
					Name:     item.Name,
					ProxyKey: key,
					Message:  "update status failed: " + err.Error(),
				})
			}
		}
		pending = append(pending, pendingProxyRel{id: created.ID, key: key, item: item})
	}

	resolveImportedProxyBackups(ctx, svc, pending, proxyNameToID, result)
	return proxyKeyToID, latencyProbeIDs
}

func registerImportedProxyNames(proxyNameToID map[string]int64, itemName, storedName string, id int64) {
	if storedName != "" {
		proxyNameToID[storedName] = id
	}
	if itemName != "" {
		proxyNameToID[itemName] = id
	}
}

func updateImportedProxyStatus(ctx context.Context, svc dataProxyWriter, current service.Proxy, item DataProxy, status string, expiresAt *time.Time) error {
	user := current.Username
	pass := current.Password
	warnDays := item.ExpiryWarnDays
	_, err := svc.UpdateProxy(ctx, current.ID, &service.UpdateProxyInput{
		Status:         status,
		ExpiresAt:      expiresAt,
		ClearExpiresAt: expiresAt == nil,
		ExpiryWarnDays: &warnDays,
		Name:           current.Name,
		Protocol:       current.Protocol,
		Host:           current.Host,
		Port:           current.Port,
		Username:       &user,
		Password:       &pass,
	})
	return err
}

func resolveImportedProxyBackups(ctx context.Context, svc dataProxyWriter, pending []pendingProxyRel, proxyNameToID map[string]int64, result *DataImportResult) {
	for i := range pending {
		rel := pending[i]
		item := rel.item
		if item.FallbackMode == "" && item.BackupProxyName == "" {
			continue
		}

		mode := item.FallbackMode
		if mode == "" {
			mode = service.FallbackModeNone
		}
		var backupID *int64
		if item.BackupProxyName != "" {
			if bid, ok := proxyNameToID[item.BackupProxyName]; ok && bid != rel.id {
				id := bid
				backupID = &id
			} else if mode == service.FallbackModeProxy {
				mode = service.FallbackModeNone
				result.Errors = append(result.Errors, DataImportError{
					Kind:     "proxy",
					Name:     item.Name,
					ProxyKey: rel.key,
					Message:  fmt.Sprintf("backup_proxy_name %q not found, fallback_mode downgraded to none", item.BackupProxyName),
				})
			}
		} else if mode == service.FallbackModeProxy {
			mode = service.FallbackModeNone
			result.Errors = append(result.Errors, DataImportError{
				Kind:     "proxy",
				Name:     item.Name,
				ProxyKey: rel.key,
				Message:  "backup_proxy_name not found, fallback_mode downgraded to none",
			})
		}

		if _, err := svc.UpdateProxy(ctx, rel.id, &service.UpdateProxyInput{
			FallbackMode:  mode,
			BackupProxyID: backupID,
			ClearBackupID: backupID == nil,
		}); err != nil {
			result.Errors = append(result.Errors, DataImportError{
				Kind:     "proxy",
				Name:     item.Name,
				ProxyKey: rel.key,
				Message:  "update backup failed: " + err.Error(),
			})
		}
	}
}
