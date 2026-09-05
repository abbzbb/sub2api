package store

import (
	"os"
	"path/filepath"
	"testing"
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
