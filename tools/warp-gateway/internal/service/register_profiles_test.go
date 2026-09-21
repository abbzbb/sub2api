package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/tools/warp-gateway/internal/register"
	"github.com/Wei-Shaw/sub2api/tools/warp-gateway/internal/store"
)

func TestRegisterProfilesUnregistersSuccessAndDropsSecrets(t *testing.T) {
	prevReg, prevUnreg := registerManyFn, unregisterDeviceFn
	t.Cleanup(func() {
		registerManyFn, unregisterDeviceFn = prevReg, prevUnreg
	})
	var unregistered []string
	registerManyFn = func(ctx context.Context, n int) ([]register.Result, error) {
		if n != 2 {
			t.Fatalf("count=%d", n)
		}
		return []register.Result{
			{Profile: store.Profile{PrivateKey: "priv-1", AccessToken: "tok-1", DeviceID: "dev-1", LicenseKey: "lic-1", Address: []string{"10.0.0.1/32"}}},
			{Profile: store.Profile{PrivateKey: "priv-2", AccessToken: "tok-2", DeviceID: "dev-2", Address: []string{"10.0.0.2/32"}}},
		}, nil
	}
	unregisterDeviceFn = func(ctx context.Context, deviceID, accessToken string) error {
		unregistered = append(unregistered, deviceID+":"+accessToken)
		return nil
	}
	mgr := &Manager{}
	got, err := mgr.RegisterProfiles(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(unregistered) != 2 || unregistered[0] != "dev-1:tok-1" || unregistered[1] != "dev-2:tok-2" {
		t.Fatalf("unregistered=%v", unregistered)
	}
	if len(got) != 2 || got[0].PrivateKey != "" || got[0].AccessToken != "" || got[0].LicenseKey != "" || got[0].DeviceID != "" {
		t.Fatalf("secrets leaked: %+v", got[0])
	}
	if len(got[0].Address) != 1 || got[0].Address[0] != "10.0.0.1/32" {
		t.Fatalf("address=%v", got[0].Address)
	}
}

func TestRegisterProfilesUnregistersPartialBatchOnError(t *testing.T) {
	prevReg, prevUnreg := registerManyFn, unregisterDeviceFn
	t.Cleanup(func() {
		registerManyFn, unregisterDeviceFn = prevReg, prevUnreg
	})
	var unregistered []string
	registerManyFn = func(ctx context.Context, n int) ([]register.Result, error) {
		return []register.Result{
			{Profile: store.Profile{PrivateKey: "priv-1", AccessToken: "tok-1", DeviceID: "dev-1"}},
		}, errors.New("register 2/2: boom")
	}
	unregisterDeviceFn = func(ctx context.Context, deviceID, accessToken string) error {
		unregistered = append(unregistered, deviceID)
		return nil
	}
	mgr := &Manager{}
	got, err := mgr.RegisterProfiles(context.Background(), 2)
	if err == nil || got != nil {
		t.Fatalf("got=%v err=%v", got, err)
	}
	if len(unregistered) != 1 || unregistered[0] != "dev-1" {
		t.Fatalf("unregistered=%v", unregistered)
	}
}
