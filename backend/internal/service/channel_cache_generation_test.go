//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestChannelInvalidationDuringInFlightLoad(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	releaseOld := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOld)
	var calls atomic.Int32
	bus := &mockChannelCachePubSub{}
	repo := &mockChannelRepository{
		listAllFn: func(context.Context) ([]Channel, error) {
			price := 9.0
			if calls.Add(1) == 1 {
				price = 1.0
				close(started)
				<-release
			}
			return []Channel{{ID: 1, Status: StatusActive, GroupIDs: []int64{10},
				ModelPricing: []ChannelModelPricing{{ID: 1, Platform: PlatformAnthropic,
					Models: []string{"claude-test"}, InputPrice: &price}},
			}}, nil
		},
		getGroupPlatformsFn: func(context.Context, []int64) (map[int64]string, error) {
			return map[int64]string{10: PlatformAnthropic}, nil
		},
	}
	subscriber := NewChannelService(repo, nil, nil, nil, bus)
	done := make(chan struct{})
	go func() {
		_ = subscriber.GetChannelModelPricing(ctx, 10, "claude-test")
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("initial cache load did not start")
	}
	// Another instance commits a new price and publishes its invalidation.
	require.NoError(t, bus.NotifyUpdate(ctx))
	releaseOld()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("initial cache load did not finish")
	}
	price := subscriber.GetChannelModelPricing(ctx, 10, "claude-test")
	require.NotNil(t, price)
	require.NotNil(t, price.InputPrice)
	require.Equal(t, 9.0, *price.InputPrice, "completed invalidation must prevent publishing the old price for later requests")
}

func TestChannelErrorCacheDoesNotSurviveLaterInvalidation(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	releaseOld := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOld)
	var calls atomic.Int32
	bus := &mockChannelCachePubSub{}
	repo := &mockChannelRepository{
		listAllFn: func(context.Context) ([]Channel, error) {
			n := calls.Add(1)
			if n == 1 {
				close(started)
				<-release
				return nil, errors.New("database down")
			}
			price := 9.0
			return []Channel{{ID: 1, Status: StatusActive, GroupIDs: []int64{10},
				ModelPricing: []ChannelModelPricing{{ID: 1, Platform: PlatformAnthropic,
					Models: []string{"claude-test"}, InputPrice: &price}},
			}}, nil
		},
		getGroupPlatformsFn: func(context.Context, []int64) (map[int64]string, error) {
			return map[int64]string{10: PlatformAnthropic}, nil
		},
	}
	subscriber := NewChannelService(repo, nil, nil, nil, bus)
	done := make(chan struct{})
	go func() {
		_, _ = subscriber.GetChannelForGroup(ctx, 10)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("initial error cache load did not start")
	}
	require.NoError(t, bus.NotifyUpdate(ctx))
	releaseOld()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("initial error cache load did not finish")
	}
	price := subscriber.GetChannelModelPricing(ctx, 10, "claude-test")
	require.NotNil(t, price)
	require.NotNil(t, price.InputPrice)
	require.Equal(t, 9.0, *price.InputPrice, "error-cache publication must not survive a later invalidation")
}
