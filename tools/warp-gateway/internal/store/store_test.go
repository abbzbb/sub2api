package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/tools/warp-gateway/internal/crypto"
)

func TestAllocatePortReservesUntilCreate(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, 43001, 43010)
	if err != nil {
		t.Fatal(err)
	}
	p1, err := s.AllocatePort(0)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.AllocatePort(0)
	if err != nil {
		t.Fatal(err)
	}
	if p1 == p2 {
		t.Fatalf("ports collided: %d", p1)
	}
	if err := s.Create(&Instance{ID: "a", Name: "a", ListenPort: p1}); err != nil {
		t.Fatal(err)
	}
	s.ReleasePort(p2)
	p3, err := s.AllocatePort(p2)
	if err != nil {
		t.Fatal(err)
	}
	if p3 != p2 {
		t.Fatalf("released port not reused: %d vs %d", p3, p2)
	}
}

func TestSocksPassAndLicenseKeyRoundTripEncrypted(t *testing.T) {
	dir := t.TempDir()
	cipher, err := crypto.NewProfileCipher("test-profile-secret")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewWithCipher(dir, 1, 10, cipher)
	if err != nil {
		t.Fatal(err)
	}
	inst := &Instance{
		ID: "sec", Name: "sec", ListenPort: 1,
		SocksAuthPass: "socks-secret",
		Profile:       Profile{LicenseKey: "license-secret", PrivateKey: "wg-secret", AccessToken: "token-secret"},
	}
	if err := s.Create(inst); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "instances.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, secret := range []string{"socks-secret", "license-secret", "wg-secret", "token-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("plaintext %q written to disk", secret)
		}
	}
	if strings.Count(text, "enc:v1:") < 4 {
		t.Fatalf("expected four enc:v1 fields, body=%s", text)
	}
	reloaded, err := NewWithCipher(dir, 1, 10, cipher)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reloaded.Get("sec")
	if err != nil {
		t.Fatal(err)
	}
	if got.SocksAuthPass != "socks-secret" || got.Profile.LicenseKey != "license-secret" {
		t.Fatalf("round trip socks=%q license=%q", got.SocksAuthPass, got.Profile.LicenseKey)
	}
}

func TestLoadLegacyPlaintextSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "instances.json")
	body := `[{"id":"legacy","name":"legacy","listen_port":2,"socks_auth_pass":"old-pass","profile":{"license_key":"old-license"}}]`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cipher, err := crypto.NewProfileCipher("test-profile-secret")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewWithCipher(dir, 1, 10, cipher)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if got.SocksAuthPass != "old-pass" || got.Profile.LicenseKey != "old-license" {
		t.Fatalf("legacy load socks=%q license=%q", got.SocksAuthPass, got.Profile.LicenseKey)
	}
	if _, err := s.Update("legacy", func(inst *Instance) {}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "old-pass") || strings.Contains(text, "old-license") {
		t.Fatalf("legacy secrets still plaintext: %s", text)
	}
	if strings.Count(text, "enc:v1:") < 2 {
		t.Fatalf("rewrite missing enc:v1: %s", text)
	}
}

func TestLoadEncryptedWithoutCipherErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "instances.json")
	if err := os.WriteFile(path, []byte(`[{"id":"x","name":"x","listen_port":1,"profile":{"private_key":"enc:v1:abc"}}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir, 1, 10); err == nil {
		t.Fatal("expected error when ciphertext present without cipher")
	}
}
