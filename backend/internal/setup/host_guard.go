package setup

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"
)

// errSetupHostNotAllowed is returned when a setup probe/install host is blocked.
// The client-facing message stays generic so this cannot be used as a scanner.
var errSetupHostNotAllowed = errors.New("host is not allowed")

// Cloud metadata and other well-known SSRF hostnames. Compared in lowercase.
var setupBlockedHostnames = map[string]struct{}{
	"metadata":                   {},
	"metadata.google.internal":   {},
	"metadata.goog":              {},
	"instance-data":              {},
	"instance-data.ec2.internal": {},
	"metadata.azure.com":         {},
	"metadata.packet.net":        {},
}

var setupBlockedCIDRs = mustParseSetupCIDRs([]string{
	"169.254.0.0/16", // link-local / cloud metadata
	"fe80::/10",      // IPv6 link-local
	"100.64.0.0/10",  // CGNAT
	"0.0.0.0/8",      // this network
	"::/128",         // IPv6 unspecified
})

var setupPrivateCIDRs = mustParseSetupCIDRs([]string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"fc00::/7",
})

func mustParseSetupCIDRs(cidrs []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("setup host guard: invalid CIDR " + c + ": " + err.Error())
		}
		out = append(out, n)
	}
	return out
}

func isSetupMetadataHostname(host string) bool {
	if host == "" {
		return true
	}
	if _, blocked := setupBlockedHostnames[host]; blocked {
		return true
	}
	// Catch metadata.google.internal.example-style suffixes and dotted aliases.
	if strings.HasPrefix(host, "metadata.") || strings.HasSuffix(host, ".metadata") {
		return true
	}
	return false
}

func ipInCIDRs(ip net.IP, cidrs []*net.IPNet) bool {
	for _, n := range cidrs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func isSetupLoopbackIP(ip net.IP) bool {
	return ip != nil && ip.IsLoopback()
}

func isSetupMetadataOrLinkLocalIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return true
	}
	return ipInCIDRs(ip, setupBlockedCIDRs)
}

func isSetupPrivateIPLiteral(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if isSetupLoopbackIP(ip) {
		return false
	}
	if ip.IsPrivate() || ipInCIDRs(ip, setupPrivateCIDRs) {
		return true
	}
	return isSetupMetadataOrLinkLocalIP(ip)
}

// validateSetupProbeHost rejects cloud-metadata, link-local, and private IP
// literals so the unauthenticated setup probes cannot be used as a scanner.
// Loopback and docker-style hostnames (postgres, redis) stay allowed so the
// existing wizard / compose UX keeps working.
func validateSetupProbeHost(host string) error {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.Trim(host, "[]")
	if host == "" {
		return errSetupHostNotAllowed
	}
	if isSetupMetadataHostname(host) {
		return errSetupHostNotAllowed
	}

	if ip := net.ParseIP(host); ip != nil {
		if isSetupLoopbackIP(ip) {
			return nil
		}
		if isSetupPrivateIPLiteral(ip) {
			return errSetupHostNotAllowed
		}
		return nil
	}

	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		// Let the later connection attempt fail with a generic client error.
		return nil
	}
	for _, ip := range ips {
		if isSetupMetadataOrLinkLocalIP(ip) {
			return errSetupHostNotAllowed
		}
	}
	return nil
}
