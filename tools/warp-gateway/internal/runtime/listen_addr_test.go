package runtime

import "testing"

func TestListenAddrJoinsIPv4AndIPv6(t *testing.T) {
	if got := listenAddr("127.0.0.1", 41001); got != "127.0.0.1:41001" {
		t.Fatalf("ipv4 addr=%q", got)
	}
	if got := listenAddr("::1", 41001); got != "[::1]:41001" {
		t.Fatalf("ipv6 addr=%q", got)
	}
	if got := listenAddr("2001:db8::1", 41001); got != "[2001:db8::1]:41001" {
		t.Fatalf("ipv6 addr=%q", got)
	}
	if got := listenAddr("[::1]", 41001); got != "[::1]:41001" {
		t.Fatalf("bracketed ipv6 addr=%q", got)
	}
}
