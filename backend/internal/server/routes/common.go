package routes

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/middleware"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

var errReadyDependencyUnavailable = errors.New("dependency unavailable")

// ReadyChecker probes process dependencies for readiness.
type ReadyChecker interface {
	Ping(ctx context.Context) error
}

type sqlReadyChecker struct {
	db *sql.DB
}

func (c sqlReadyChecker) Ping(ctx context.Context) error {
	if c.db == nil {
		return errReadyDependencyUnavailable
	}
	return c.db.PingContext(ctx)
}

type redisReadyChecker struct {
	client *redis.Client
}

func (c redisReadyChecker) Ping(ctx context.Context) error {
	if c.client == nil {
		return errReadyDependencyUnavailable
	}
	return c.client.Ping(ctx).Err()
}

// HealthDependencies are optional probes used by /health/ready.
type HealthDependencies struct {
	DB          ReadyChecker
	Redis       ReadyChecker
	RedisClient *redis.Client
}

// NewHealthDependencies builds readiness probes from the process SQL and Redis clients.
func NewHealthDependencies(db *sql.DB, redisClient *redis.Client) HealthDependencies {
	return HealthDependencies{
		DB:          sqlReadyChecker{db: db},
		Redis:       redisReadyChecker{client: redisClient},
		RedisClient: redisClient,
	}
}

// RegisterCommonRoutes 注册通用路由（健康检查、状态等）
func RegisterCommonRoutes(r *gin.Engine, deps HealthDependencies) {
	live := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}

	// Liveness: process is up. Keep /health for existing probes.
	r.GET("/health", live)
	r.GET("/health/live", live)
	r.GET("/health/ready", readyHandler(deps))

	// Claude Code 遥测日志（忽略，直接返回200）。有 Redis 时限流，避免空 200 被用来刷连接。
	eventLog := func(c *gin.Context) {
		c.Status(http.StatusOK)
	}
	if deps.RedisClient != nil {
		r.POST("/api/event_logging/batch", middleware.NewRateLimiter(deps.RedisClient).LimitWithOptions(
			"event-logging-batch", 30, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailOpen,
			},
		), eventLog)
	} else {
		r.POST("/api/event_logging/batch", eventLog)
	}

	// Setup status endpoint (always returns needs_setup: false in normal mode)
	// This is used by the frontend to detect when the service has restarted after setup
	r.GET("/setup/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"code": 0,
			"data": gin.H{
				"needs_setup": false,
				"step":        "completed",
			},
		})
	})
}

func readyHandler(deps HealthDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()

		checks := gin.H{
			"database": "ok",
			"redis":    "ok",
		}
		ready := true
		if err := pingReady(ctx, deps.DB); err != nil {
			checks["database"] = "unavailable"
			ready = false
		}
		if err := pingReady(ctx, deps.Redis); err != nil {
			checks["redis"] = "unavailable"
			ready = false
		}
		if !ready {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "checks": checks})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "checks": checks})
	}
}

func pingReady(ctx context.Context, checker ReadyChecker) error {
	if checker == nil {
		return errReadyDependencyUnavailable
	}
	return checker.Ping(ctx)
}
