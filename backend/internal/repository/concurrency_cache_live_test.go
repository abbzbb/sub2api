package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestLiveLeaseReplacesRegularSlotsAndCountsTowardLimits(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	regular := NewConcurrencyCache(client, 15, 900)
	live, ok := regular.(service.LiveConcurrencyCache)
	require.True(t, ok)
	ctx := context.Background()

	accountAcquired, err := regular.AcquireAccountSlot(ctx, 10, 1, "regular-account")
	require.NoError(t, err)
	require.True(t, accountAcquired)
	userAcquired, err := regular.AcquireUserSlot(ctx, 20, 1, "regular-user")
	require.NoError(t, err)
	require.True(t, userAcquired)

	acquired, err := live.AcquireLiveLease(ctx, 10, 1, 20, 1, 30, "live-lease", true)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, regular.ReleaseAccountSlot(ctx, 10, "regular-account"))
	require.NoError(t, regular.ReleaseUserSlot(ctx, 20, "regular-user"))

	accountCount, err := regular.GetAccountConcurrency(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, 1, accountCount)
	userCount, err := regular.GetUserConcurrency(ctx, 20)
	require.NoError(t, err)
	require.Equal(t, 1, userCount)
	accountAcquired, err = regular.AcquireAccountSlot(ctx, 10, 1, "ordinary-blocked")
	require.NoError(t, err)
	require.False(t, accountAcquired)

	refreshed, err := live.RefreshLiveLease(ctx, 10, 20, 30, "live-lease")
	require.NoError(t, err)
	require.True(t, refreshed)
	require.NoError(t, live.ReleaseLiveLease(ctx, 10, 20, 30, "live-lease"))
	accountAcquired, err = regular.AcquireAccountSlot(ctx, 10, 1, "ordinary-allowed")
	require.NoError(t, err)
	require.True(t, accountAcquired)
}

func TestLiveLeaseExpiresWithoutRefresh(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	regular := NewConcurrencyCache(client, 15, 900)
	live, ok := regular.(service.LiveConcurrencyCache)
	require.True(t, ok)
	ctx := context.Background()

	acquired, err := live.AcquireLiveLease(ctx, 10, 1, 20, 1, 30, "expired-live", false)
	require.NoError(t, err)
	require.True(t, acquired)

	redisServer.FastForward(61 * time.Second)
	acquired, err = regular.AcquireAccountSlot(ctx, 10, 1, "ordinary-after-expiry")
	require.NoError(t, err)
	require.True(t, acquired)
	refreshed, err := live.RefreshLiveLease(ctx, 10, 20, 30, "expired-live")
	require.NoError(t, err)
	require.False(t, refreshed)
}

func TestAcquireAccountSlotReapsDeadInstanceSlots(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	cache, ok := NewConcurrencyCache(client, 15, 900).(*concurrencyCache)
	require.True(t, ok)
	ctx := context.Background()
	accountID := int64(44)
	key := accountSlotKey(accountID)
	now := time.Now().Unix()
	require.NoError(t, client.ZAdd(ctx, key, redis.Z{Score: float64(now - int64(instanceHeartbeatTTL/time.Second) - 1), Member: "rdead01-1"}).Err())

	acquired, err := cache.AcquireAccountSlot(ctx, accountID, 1, "rlive01-1")
	require.NoError(t, err)
	require.True(t, acquired, "dead instance slot without heartbeat must be reclaimed")

	members, err := client.ZRange(ctx, key, 0, -1).Result()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"rlive01-1"}, members)
}

func TestAcquireAccountSlotReapsExpiredHeartbeatWithoutRenewal(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	cache, ok := NewConcurrencyCache(client, 15, 900).(*concurrencyCache)
	require.True(t, ok)
	ctx := context.Background()
	accountID := int64(45)
	key := accountSlotKey(accountID)
	now := time.Now().Unix()
	require.NoError(t, client.ZAdd(ctx, key, redis.Z{Score: float64(now - int64(instanceHeartbeatTTL/time.Second) - 1), Member: "rA-1"}).Err())
	cache.touchInstanceHeartbeat(ctx, "rA")
	require.NoError(t, client.Del(ctx, instanceHeartbeatKey("rA")).Err())

	acquired, err := cache.AcquireAccountSlot(ctx, accountID, 1, "rB-1")
	require.NoError(t, err)
	require.True(t, acquired, "expired heartbeat and stale score must be treated as dead")
	members, err := client.ZRange(ctx, key, 0, -1).Result()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"rB-1"}, members)
}

func TestAcquireAccountSlotKeepsInFlightWhenHeartbeatRenewed(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	cache, ok := NewConcurrencyCache(client, 15, 900).(*concurrencyCache)
	require.True(t, ok)
	ctx := context.Background()
	accountID := int64(46)
	key := accountSlotKey(accountID)
	now := time.Now().Unix()
	require.NoError(t, client.ZAdd(ctx, key, redis.Z{Score: float64(now), Member: "rA-1"}).Err())
	cache.startInstanceHeartbeat(ctx, "rA", 20*time.Millisecond)
	t.Cleanup(cache.StopInstanceHeartbeat)

	redisServer.FastForward(instanceHeartbeatTTL + time.Second)
	time.Sleep(50 * time.Millisecond)

	acquired, err := cache.AcquireAccountSlot(ctx, accountID, 1, "rB-1")
	require.NoError(t, err)
	require.False(t, acquired, "renewed heartbeat must keep in-flight peer slots")
	members, err := client.ZRange(ctx, key, 0, -1).Result()
	require.NoError(t, err)
	require.Contains(t, members, "rA-1")
	require.NotContains(t, members, "rB-1")
}

func TestAcquireAccountSlotKeepsPeerWithoutHeartbeatWhenScoreFresh(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	cache, ok := NewConcurrencyCache(client, 15, 900).(*concurrencyCache)
	require.True(t, ok)
	ctx := context.Background()
	accountID := int64(47)
	key := accountSlotKey(accountID)
	now := time.Now().Unix()
	require.NoError(t, client.ZAdd(ctx, key, redis.Z{Score: float64(now), Member: "roldbin-1"}).Err())

	acquired, err := cache.AcquireAccountSlot(ctx, accountID, 1, "rnewbin-1")
	require.NoError(t, err)
	require.False(t, acquired, "rolling-upgrade peer with fresh slot score must not be reaped")
	members, err := client.ZRange(ctx, key, 0, -1).Result()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"roldbin-1"}, members)
}

func TestAcquireAccountSlotDoesNotReapWhenNotFull(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	cache, ok := NewConcurrencyCache(client, 15, 900).(*concurrencyCache)
	require.True(t, ok)
	ctx := context.Background()
	accountID := int64(48)
	key := accountSlotKey(accountID)
	stale := time.Now().Unix() - int64(instanceHeartbeatTTL/time.Second) - 5
	require.NoError(t, client.ZAdd(ctx, key, redis.Z{Score: float64(stale), Member: "rdead02-1"}).Err())

	acquired, err := cache.AcquireAccountSlot(ctx, accountID, 2, "rlive02-1")
	require.NoError(t, err)
	require.True(t, acquired)
	members, err := client.ZRange(ctx, key, 0, -1).Result()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"rdead02-1", "rlive02-1"}, members, "reap only runs when the slot is full")
	require.Equal(t, 0, cache.reapCalls)
	require.Equal(t, 1, cache.acquireScripts)
}

func TestAcquireAccountSlotDoesNotRetryWhenFullWithoutDead(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	cache, ok := NewConcurrencyCache(client, 15, 900).(*concurrencyCache)
	require.True(t, ok)
	ctx := context.Background()
	accountID := int64(49)
	key := accountSlotKey(accountID)
	now := time.Now().Unix()
	require.NoError(t, client.ZAdd(ctx, key, redis.Z{Score: float64(now), Member: "rlive03-1"}).Err())

	acquired, err := cache.AcquireAccountSlot(ctx, accountID, 1, "rnew03-1")
	require.NoError(t, err)
	require.False(t, acquired)
	require.Equal(t, 1, cache.reapCalls)
	require.Equal(t, 1, cache.acquireScripts, "full slot with no dead peers must not retry acquire")
	members, err := client.ZRange(ctx, key, 0, -1).Result()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"rlive03-1"}, members)
}

func TestRequestIDInstancePrefix(t *testing.T) {
	require.Equal(t, "rlive01", requestIDInstancePrefix("rlive01-1"))
	require.Equal(t, "keep", requestIDInstancePrefix("keep-1"))
	require.Equal(t, "keep", normalizeInstancePrefix("keep-"))
	require.Equal(t, "", requestIDInstancePrefix("noshift"))
}
