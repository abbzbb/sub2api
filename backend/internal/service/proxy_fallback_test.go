//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func mkProxy(id int64, mode string, backup *int64, expiresInDays *int, now time.Time) Proxy {
	p := Proxy{ID: id, Status: StatusActive, FallbackMode: mode, BackupProxyID: backup}
	if expiresInDays != nil {
		t := now.AddDate(0, 0, *expiresInDays)
		p.ExpiresAt = &t
	}
	return p
}
func i64(v int64) *int64 { return &v }
func di(v int) *int      { return &v }

func TestResolveFallbackTarget(t *testing.T) {
	now := time.Now()
	t.Run("none keeps original", func(t *testing.T) {
		a := mkProxy(1, FallbackModeNone, nil, di(-1), now)
		by := map[int64]Proxy{1: a}
		target, change := ResolveProxyFallbackTarget(a, by, now)
		require.False(t, change)
		require.Nil(t, target)
	})
	t.Run("direct -> nil target, change", func(t *testing.T) {
		a := mkProxy(1, FallbackModeDirect, nil, di(-1), now)
		by := map[int64]Proxy{1: a}
		target, change := ResolveProxyFallbackTarget(a, by, now)
		require.True(t, change)
		require.Nil(t, target)
	})
	t.Run("proxy -> healthy backup", func(t *testing.T) {
		b := mkProxy(2, FallbackModeNone, nil, di(30), now)
		a := mkProxy(1, FallbackModeProxy, i64(2), di(-1), now)
		by := map[int64]Proxy{1: a, 2: b}
		target, change := ResolveProxyFallbackTarget(a, by, now)
		require.True(t, change)
		require.NotNil(t, target)
		require.Equal(t, int64(2), *target)
	})
	t.Run("chain A->B(expired)->C(healthy)", func(t *testing.T) {
		c := mkProxy(3, FallbackModeNone, nil, di(30), now)
		b := mkProxy(2, FallbackModeProxy, i64(3), di(-1), now)
		a := mkProxy(1, FallbackModeProxy, i64(2), di(-1), now)
		by := map[int64]Proxy{1: a, 2: b, 3: c}
		target, change := ResolveProxyFallbackTarget(a, by, now)
		require.True(t, change)
		require.Equal(t, int64(3), *target)
	})
	t.Run("cycle A->B->A keeps original", func(t *testing.T) {
		b := mkProxy(2, FallbackModeProxy, i64(1), di(-1), now)
		a := mkProxy(1, FallbackModeProxy, i64(2), di(-1), now)
		by := map[int64]Proxy{1: a, 2: b}
		target, change := ResolveProxyFallbackTarget(a, by, now)
		require.False(t, change)
		require.Nil(t, target)
	})
	t.Run("chain tail direct fallback", func(t *testing.T) {
		b := mkProxy(2, FallbackModeDirect, nil, di(-1), now)
		a := mkProxy(1, FallbackModeProxy, i64(2), di(-1), now)
		by := map[int64]Proxy{1: a, 2: b}
		target, change := ResolveProxyFallbackTarget(a, by, now)
		require.True(t, change)
		require.Nil(t, target)
	})
}

func TestResolveFallbackTargetSkipsUnavailableBackups(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)

	t.Run("inactive backup with no further chain is unresolved", func(t *testing.T) {
		backupID := int64(2)
		primary := Proxy{ID: 1, Status: StatusActive, FallbackMode: FallbackModeProxy, BackupProxyID: &backupID}
		backup := Proxy{ID: backupID, Status: StatusInactive, FallbackMode: FallbackModeNone, ExpiresAt: &future}
		target, change := ResolveProxyFallbackTarget(primary, map[int64]Proxy{
			primary.ID: primary,
			backup.ID:  backup,
		}, now)
		require.False(t, change)
		require.Nil(t, target)
	})

	t.Run("inactive then active grandchild", func(t *testing.T) {
		childID := int64(2)
		grandchildID := int64(3)
		primary := Proxy{ID: 1, Status: StatusActive, FallbackMode: FallbackModeProxy, BackupProxyID: &childID}
		child := Proxy{ID: childID, Status: StatusInactive, FallbackMode: FallbackModeProxy, BackupProxyID: &grandchildID, ExpiresAt: &future}
		grandchild := Proxy{ID: grandchildID, Status: StatusActive, FallbackMode: FallbackModeNone, ExpiresAt: &future}
		target, change := ResolveProxyFallbackTarget(primary, map[int64]Proxy{
			primary.ID:    primary,
			child.ID:      child,
			grandchild.ID: grandchild,
		}, now)
		require.True(t, change)
		require.NotNil(t, target)
		require.Equal(t, grandchildID, *target)
	})

	t.Run("error status skipped", func(t *testing.T) {
		errorID := int64(2)
		healthyID := int64(3)
		primary := Proxy{ID: 1, Status: StatusActive, FallbackMode: FallbackModeProxy, BackupProxyID: &errorID}
		errNode := Proxy{ID: errorID, Status: StatusError, FallbackMode: FallbackModeProxy, BackupProxyID: &healthyID, ExpiresAt: &future}
		healthy := Proxy{ID: healthyID, Status: StatusActive, FallbackMode: FallbackModeNone, ExpiresAt: &future}
		target, change := ResolveProxyFallbackTarget(primary, map[int64]Proxy{
			primary.ID: primary,
			errNode.ID: errNode,
			healthy.ID: healthy,
		}, now)
		require.True(t, change)
		require.NotNil(t, target)
		require.Equal(t, healthyID, *target)
	})

	t.Run("error status with no further chain is unresolved", func(t *testing.T) {
		errorID := int64(2)
		primary := Proxy{ID: 1, Status: StatusActive, FallbackMode: FallbackModeProxy, BackupProxyID: &errorID}
		errNode := Proxy{ID: errorID, Status: StatusError, FallbackMode: FallbackModeNone, ExpiresAt: &future}
		target, change := ResolveProxyFallbackTarget(primary, map[int64]Proxy{
			primary.ID: primary,
			errNode.ID: errNode,
		}, now)
		require.False(t, change)
		require.Nil(t, target)
	})

	t.Run("expired backup skipped", func(t *testing.T) {
		past := now.Add(-time.Hour)
		future := now.Add(24 * time.Hour)
		expiredID := int64(2)
		healthyID := int64(3)
		primary := Proxy{ID: 1, Status: StatusActive, FallbackMode: FallbackModeProxy, BackupProxyID: &expiredID}
		expired := Proxy{ID: expiredID, Status: StatusActive, FallbackMode: FallbackModeProxy, BackupProxyID: &healthyID, ExpiresAt: &past}
		healthy := Proxy{ID: healthyID, Status: StatusActive, FallbackMode: FallbackModeNone, ExpiresAt: &future}
		target, change := ResolveProxyFallbackTarget(primary, map[int64]Proxy{
			primary.ID: primary,
			expired.ID: expired,
			healthy.ID: healthy,
		}, now)
		require.True(t, change)
		require.NotNil(t, target)
		require.Equal(t, healthyID, *target)
	})

	t.Run("direct on unavailable node", func(t *testing.T) {
		backupID := int64(2)
		primary := Proxy{ID: 1, Status: StatusActive, FallbackMode: FallbackModeProxy, BackupProxyID: &backupID}
		backup := Proxy{ID: backupID, Status: StatusInactive, FallbackMode: FallbackModeDirect, ExpiresAt: &future}
		target, change := ResolveProxyFallbackTarget(primary, map[int64]Proxy{
			primary.ID: primary,
			backup.ID:  backup,
		}, now)
		require.True(t, change)
		require.Nil(t, target)
	})
}
