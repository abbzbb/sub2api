//go:build unit

package service

import (
	"strings"
	"testing"
	"unicode"
)

func TestGenerateOutTradeNoUsesLongerCryptoSuffix(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 20; i++ {
		got := generateOutTradeNo()
		if !strings.HasPrefix(got, orderIDPrefix) {
			t.Fatalf("prefix = %q", got)
		}
		suffix := strings.TrimPrefix(got, orderIDPrefix)
		if len(suffix) != 8+16 { // YYYYMMDD + 16 random
			t.Fatalf("id %q suffix len = %d, want 24", got, len(suffix))
		}
		for _, r := range suffix[8:] {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				t.Fatalf("non-alnum in suffix: %q", got)
			}
		}
		if _, ok := seen[got]; ok {
			t.Fatalf("duplicate out_trade_no %q", got)
		}
		seen[got] = struct{}{}
	}
}
