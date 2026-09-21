package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadFromEnvAutoRotateDuplicateExitIP(t *testing.T) {
	t.Setenv("WARP_GATEWAY_AUTO_ROTATE_DUPLICATE_EXIT_IP", "")
	t.Setenv("WARP_GATEWAY_AUTO_ROTATE_COOLDOWN", "")
	cfg := Default()
	if !cfg.AutoRotateDuplicateExitIP {
		t.Fatal("default auto-rotate should be on")
	}
	if cfg.AutoRotateCooldown != 15*time.Minute {
		t.Fatalf("cooldown=%s", cfg.AutoRotateCooldown)
	}
	t.Setenv("WARP_GATEWAY_AUTO_ROTATE_DUPLICATE_EXIT_IP", "false")
	t.Setenv("WARP_GATEWAY_AUTO_ROTATE_COOLDOWN", "30m")
	cfg = LoadFromEnv()
	if cfg.AutoRotateDuplicateExitIP {
		t.Fatal("env false should disable auto-rotate")
	}
	if cfg.AutoRotateCooldown != 30*time.Minute {
		t.Fatalf("cooldown=%s", cfg.AutoRotateCooldown)
	}
}

func TestProfileSecretDoesNotFallBackToToken(t *testing.T) {
	cfg := Config{Token: "api-token", ProfileKey: ""}
	if cfg.ProfileSecret() != "" {
		t.Fatalf("secret=%q want empty (must not use token)", cfg.ProfileSecret())
	}
	cfg.ProfileKey = "explicit-key"
	if cfg.ProfileSecret() != "explicit-key" {
		t.Fatalf("secret=%q", cfg.ProfileSecret())
	}
}

func TestValidateRejectsNonLoopbackWithoutToken(t *testing.T) {
	cfg := Default()
	cfg.Listen = "0.0.0.0:19798"
	cfg.Token = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for 0.0.0.0 without token")
	}
	cfg.Token = "secret"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsBarePortListenWithoutToken(t *testing.T) {
	cfg := Default()
	cfg.Token = ""
	cfg.Listen = ":19798"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for :19798 without token")
	}
	cfg.Listen = "[::]:19798"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for [::]:19798 without token")
	}
	cfg.Token = "secret"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsLoopbackWithoutTokenByDefault(t *testing.T) {
	cfg := Default()
	cfg.Listen = "127.0.0.1:19798"
	cfg.Token = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for loopback without token")
	}
	cfg.AllowInsecureNoAuth = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFromEnvAllowInsecureNoAuth(t *testing.T) {
	t.Setenv("WARP_GATEWAY_ALLOW_INSECURE_NO_AUTH", "1")
	cfg := LoadFromEnv()
	if !cfg.AllowInsecureNoAuth {
		t.Fatal("expected dev switch from env")
	}
	cfg.Listen = "127.0.0.1:19798"
	cfg.Token = ""
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Listen = "0.0.0.0:19798"
	if err := cfg.Validate(); err == nil {
		t.Fatal("dev switch must not allow empty token on a public listen")
	}
}

func TestValidateRejectsClientCAWithoutTLSCertKey(t *testing.T) {
	cfg := Default()
	cfg.Listen = "0.0.0.0:19798"
	cfg.Token = ""
	cfg.ClientCAFile = "/tmp/client-ca.pem"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for client CA without TLS cert/key")
	}
	cfg.TLSCertFile = "/tmp/server.pem"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for cert without key")
	}
	cfg.TLSKeyFile = "/tmp/server.key"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsTLSCertWithoutKey(t *testing.T) {
	cfg := Default()
	cfg.Listen = "127.0.0.1:19798"
	cfg.Token = ""
	cfg.TLSCertFile = "/tmp/server.pem"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for cert without key")
	}
	cfg.TLSCertFile = ""
	cfg.TLSKeyFile = "/tmp/server.key"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for key without cert")
	}
}

func TestEnsureProfileKeyCreates0600File(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{DataDir: dir}
	if err := EnsureProfileKey(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ProfileKey == "" {
		t.Fatal("expected generated profile key")
	}
	info, err := os.Stat(filepath.Join(dir, profileKeyFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm=%o want 0600", info.Mode().Perm())
	}
	again := Config{DataDir: dir}
	if err := EnsureProfileKey(&again); err != nil {
		t.Fatal(err)
	}
	if again.ProfileKey != cfg.ProfileKey {
		t.Fatalf("reloaded key mismatch")
	}
}
