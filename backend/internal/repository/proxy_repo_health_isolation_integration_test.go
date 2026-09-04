//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// 真库回归：isolate（仅状态 + status 守卫）与 recover（计数列 + status + isolated_by 守卫）
// 两种参数组合都必须能执行。历史版本因缺逗号 / 占位符错位 / 引用不存在的 generation 列，
// recover 在生产中永远报错，且被单测 stub 掩盖。
func (s *ProxyRepoSuite) TestUpdateStatusWithHealthIsolation_IsolateThenRecover() {
	proxy := &service.Proxy{
		Name:     "health-iso",
		Protocol: "http",
		Host:     "127.0.0.1",
		Port:     18080,
		Status:   service.StatusActive,
	}
	s.Require().NoError(s.repo.Create(s.ctx, proxy))

	// isolate: status-only, guarded by status='active'
	updated, err := s.repo.UpdateStatusWithHealthIsolation(
		s.ctx, proxy.ID, service.StatusInactive, 0, nil, service.ProxyHealthIsolatedByHealth,
		service.StatusActive, nil, false,
	)
	s.Require().NoError(err)
	s.Require().True(updated)

	got, err := s.repo.GetByID(s.ctx, proxy.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.StatusInactive, got.Status)

	// isolate again must be a no-op (status guard fails), not an error
	updated, err = s.repo.UpdateStatusWithHealthIsolation(
		s.ctx, proxy.ID, service.StatusInactive, 0, nil, service.ProxyHealthIsolatedByHealth,
		service.StatusActive, nil, false,
	)
	s.Require().NoError(err)
	s.Require().False(updated)

	// mark the audit column so the recover guard can match
	now := time.Now().UTC().Truncate(time.Second)
	s.Require().NoError(s.repo.UpdateHealthAudit(s.ctx, proxy.ID, 3, &now, service.ProxyHealthIsolatedByHealth))

	// recover: counters + status guard + isolated_by guard
	mark := service.ProxyHealthIsolatedByHealth
	updated, err = s.repo.UpdateStatusWithHealthIsolation(
		s.ctx, proxy.ID, service.StatusActive, 0, &now, "",
		service.StatusInactive, &mark, true,
	)
	s.Require().NoError(err)
	s.Require().True(updated)

	got, err = s.repo.GetByID(s.ctx, proxy.ID)
	s.Require().NoError(err)
	s.Require().Equal(service.StatusActive, got.Status)

	failCount, _, isolatedBy, err := s.repo.GetHealthAudit(s.ctx, proxy.ID)
	s.Require().NoError(err)
	s.Require().Equal(0, failCount)
	s.Require().Equal("", isolatedBy)

	// recover on an admin-disabled proxy (isolated_by empty) must not flip it back
	got.Status = service.StatusInactive
	s.Require().NoError(s.repo.Update(s.ctx, got))
	updated, err = s.repo.UpdateStatusWithHealthIsolation(
		s.ctx, proxy.ID, service.StatusActive, 0, &now, "",
		service.StatusInactive, &mark, true,
	)
	s.Require().NoError(err)
	s.Require().False(updated)
}
