package provider

import (
	"fmt"
	"strings"
	"time"
)

const paymentNotifyFreshnessWindow = 15 * time.Minute

func rejectStalePaymentNotify(raw string, now time.Time) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed, ok := parsePaymentNotifyTime(raw)
	if !ok {
		return fmt.Errorf("payment notify time is invalid")
	}
	if now.IsZero() {
		now = time.Now()
	}
	delta := now.Sub(parsed)
	if delta < -paymentNotifyFreshnessWindow || delta > paymentNotifyFreshnessWindow {
		return fmt.Errorf("payment notify is outside the freshness window")
	}
	return nil
}

func parsePaymentNotifyTime(raw string) (time.Time, bool) {
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02+15:04:05",
		time.DateTime,
	}
	for _, layout := range layouts {
		if parsed, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return parsed, true
		}
	}
	if unix, err := parseUnixMaybe(raw); err == nil {
		return unix, true
	}
	return time.Time{}, false
}

func parseUnixMaybe(raw string) (time.Time, error) {
	var n int64
	if _, err := fmt.Sscan(raw, &n); err != nil {
		return time.Time{}, err
	}
	if n <= 0 {
		return time.Time{}, fmt.Errorf("invalid unix time")
	}
	if n > 1e12 {
		return time.UnixMilli(n), nil
	}
	return time.Unix(n, 0), nil
}
