package health

import "testing"

func TestSocksProxyAddrJoinsIPv4AndIPv6(t *testing.T) {
	if got := socksProxyAddr("127.0.0.1", 41001); got != "127.0.0.1:41001" {
		t.Fatalf("ipv4 addr=%q", got)
	}
	if got := socksProxyAddr("::1", 41001); got != "[::1]:41001" {
		t.Fatalf("ipv6 addr=%q", got)
	}
	if got := socksProxyAddr("2001:db8::1", 41001); got != "[2001:db8::1]:41001" {
		t.Fatalf("ipv6 addr=%q", got)
	}
	if got := socksProxyAddr("[::1]", 41001); got != "[::1]:41001" {
		t.Fatalf("bracketed ipv6 addr=%q", got)
	}
}
