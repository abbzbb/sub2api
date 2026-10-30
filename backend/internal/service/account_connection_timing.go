package service

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	"github.com/gin-gonic/gin"
)

type accountTestStartKey struct{}

// WithAccountTestTimingStart includes handler validation in the test lifetime.
func WithAccountTestTimingStart(ctx context.Context, start time.Time) context.Context {
	return context.WithValue(ctx, accountTestStartKey{}, start)
}

func beginAccountConnectionTiming(c *gin.Context) (*requesttiming.Collector, time.Time) {
	start, ok := c.Request.Context().Value(accountTestStartKey{}).(time.Time)
	if !ok {
		start = time.Now()
	}
	collector := requesttiming.New(start, 0)
	c.Request = c.Request.WithContext(requesttiming.With(c.Request.Context(), collector))
	return collector, start
}

func (s *AccountTestService) finishAccountConnectionTiming(c *gin.Context, collector *requesttiming.Collector, err error) {
	outcome := "success"
	var timeout net.Error
	switch {
	case errors.Is(c.Request.Context().Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded), errors.As(err, &timeout) && timeout.Timeout():
		outcome = "timeout"
	case errors.Is(c.Request.Context().Err(), context.Canceled), errors.Is(err, context.Canceled):
		outcome = "canceled"
	case err != nil:
		outcome = "error"
	}
	requesttiming.Outcome(c.Request.Context(), outcome, outcome == "canceled")
	collector.WhenFinished(func(d requesttiming.Snapshot) {
		timing := requesttiming.AccountTestProjection(d)
		s.sendEvent(c, TestEvent{Type: "timing", Timing: &timing})
	})
	collector.Finish(c.Writer.Status(), outcome == "canceled")
}
