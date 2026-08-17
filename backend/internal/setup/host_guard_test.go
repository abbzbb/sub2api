package setup

import (
	"net"
	"testing"
)

func TestValidateSetupProbeHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		host    string
		wantErr bool
	}{
		{name: "localhost allowed for wizard", host: "localhost"},
		{name: "loopback ipv4 allowed", host: "127.0.0.1"},
		{name: "loopback ipv6 allowed", host: "::1"},
		{name: "docker service name allowed", host: "postgres"},
		{name: "redis service name allowed", host: "redis"},
		{name: "public hostname allowed", host: "example.com"},
		{name: "public ipv4 allowed", host: "1.1.1.1"},
		{name: "empty rejected", host: "", wantErr: true},
		{name: "rfc1918 10 rejected", host: "10.0.0.1", wantErr: true},
		{name: "rfc1918 192 rejected", host: "192.168.1.10", wantErr: true},
		{name: "rfc1918 172 rejected", host: "172.16.0.2", wantErr: true},
		{name: "link local rejected", host: "169.254.169.254", wantErr: true},
		{name: "unspecified rejected", host: "0.0.0.0", wantErr: true},
		{name: "metadata hostname rejected", host: "metadata.google.internal", wantErr: true},
		{name: "metadata alias rejected", host: "metadata", wantErr: true},
		{name: "ec2 instance-data rejected", host: "instance-data.ec2.internal", wantErr: true},
		{name: "cgnat rejected", host: "100.64.0.1", wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateSetupProbeHost(tc.host)
			if tc.wantErr && err == nil {
				t.Fatalf("validateSetupProbeHost(%q) = nil, want error", tc.host)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateSetupProbeHost(%q) = %v, want nil", tc.host, err)
			}
		})
	}
}

func TestIsSetupPrivateIPLiteral(t *testing.T) {
	t.Parallel()
	if isSetupPrivateIPLiteral(net.ParseIP("127.0.0.1")) {
		t.Fatal("loopback should not be treated as a blocked private literal")
	}
	if !isSetupPrivateIPLiteral(net.ParseIP("10.1.2.3")) {
		t.Fatal("10/8 should be a blocked private literal")
	}
	if !isSetupPrivateIPLiteral(net.ParseIP("169.254.1.1")) {
		t.Fatal("link-local should be blocked")
	}
}
