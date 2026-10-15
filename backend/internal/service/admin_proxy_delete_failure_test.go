//go:build unit

package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/mihomo"
	"github.com/stretchr/testify/require"
)

func TestDeleteProxy_NodeDeactivationFailureDoesNotClearBindings(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	require.NoError(t, os.WriteFile(settings, []byte(`{"static_bindings":{"review-delete-node":{"port":32001,"enabled":true}}}`), 0600))
	manager := mihomo.New(dir)
	t.Cleanup(manager.Close)
	// Make persisting a deactivation fail without invoking a real kernel.
	require.NoError(t, os.Remove(settings))
	require.NoError(t, os.Mkdir(settings, 0700))
	repo := &proxyRepoStub{proxy: &Proxy{ID: 7, Name: "static-proxy", MihomoNodeID: "review-delete-node"}}
	svc := &adminServiceImpl{proxyRepo: repo}
	err := svc.DeleteProxy(context.Background(), 7)
	require.ErrorContains(t, err, "cannot save static node bindings")
	require.Empty(t, repo.clearedBindingIDs)
	require.Empty(t, repo.deletedIDs)
}

func TestDeleteProxy_DatabaseFailureDoesNotClearBindingsInService(t *testing.T) {
	repo := &proxyRepoStub{deleteErr: os.ErrPermission}
	svc := &adminServiceImpl{proxyRepo: repo}
	require.ErrorIs(t, svc.DeleteProxy(context.Background(), 7), os.ErrPermission)
	require.Empty(t, repo.clearedBindingIDs, "binding cleanup belongs to the repository transaction")
}
