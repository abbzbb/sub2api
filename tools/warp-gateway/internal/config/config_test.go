package config

import (
	"os"
	"path/filepath"
	"testing"
)

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

func TestValidateAllowsLoopbackWithoutToken(t *testing.T) {
	cfg := Default()
	cfg.Listen = "127.0.0.1:19798"
	cfg.Token = ""
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
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
