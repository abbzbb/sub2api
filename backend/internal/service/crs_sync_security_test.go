package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCRSLoginDoesNotEchoUpstreamBody(t *testing.T) {
	t.Parallel()

	const leak = "INTERNAL_HTML_SECRET_BODY"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("<html>" + leak + "</html>"))
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := crsLogin(ctx, server.Client(), server.URL, "user", "pass")
	if err == nil {
		t.Fatal("expected login error")
	}
	if strings.Contains(err.Error(), leak) || strings.Contains(err.Error(), "<html>") {
		t.Fatalf("login error echoed upstream body: %v", err)
	}
}

func TestCRSExportDoesNotEchoUpstreamBody(t *testing.T) {
	t.Parallel()

	const leak = "EXPORT_UPSTREAM_BODY_LEAK"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(leak + ` {"access_token":"sk-secret"}`))
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := crsExportAccounts(ctx, server.Client(), server.URL, "token")
	if err == nil {
		t.Fatal("expected export error")
	}
	if strings.Contains(err.Error(), leak) || strings.Contains(err.Error(), "sk-secret") {
		t.Fatalf("export error echoed upstream body: %v", err)
	}
}

func TestCRSPreviewAccountOmitsCredentials(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf(CRSPreviewAccount{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.ToLower(field.Name + " " + field.Tag.Get("json"))
		for _, banned := range []string{"credential", "secret", "token", "password", "api_key", "apikey"} {
			if strings.Contains(name, banned) {
				t.Fatalf("preview account field %s looks like a credential", field.Name)
			}
		}
	}

	raw, err := json.Marshal(CRSPreviewAccount{
		CRSAccountID: "crs-1",
		Kind:         "claude",
		Name:         "demo",
		Platform:     PlatformAnthropic,
		Type:         AccountTypeOAuth,
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	lower := strings.ToLower(string(raw))
	for _, banned := range []string{"credential", "secret", "access_token", "refresh_token", "api_key"} {
		if strings.Contains(lower, banned) {
			t.Fatalf("preview JSON leaked %q: %s", banned, raw)
		}
	}
}
