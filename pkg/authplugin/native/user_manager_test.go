package native

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

func TestNativeUserManager_SupportsFeature(t *testing.T) {
	m := &NativeUserManager{}
	tests := []struct {
		feature authplugin.UserFeature
		want    bool
	}{
		{authplugin.FeatureUserSearch, true},
		{authplugin.FeatureUserGroups, true},
		{authplugin.FeatureUserAttributes, false},
		{authplugin.FeatureBulkOperations, false},
	}
	for _, tt := range tests {
		if got := m.SupportsFeature(tt.feature); got != tt.want {
			t.Errorf("SupportsFeature(%v) = %v, want %v", tt.feature, got, tt.want)
		}
	}
}

func TestNativeUserManager_Name(t *testing.T) {
	m := &NativeUserManager{}
	if got := m.Name(); got != "native" {
		t.Errorf("Name() = %v, want %v", got, "native")
	}
}

func TestNativeUserManager_GetUserByExternalID_InvalidFormat(t *testing.T) {
	m := &NativeUserManager{}
	ctx := context.Background()

	_, err := m.GetUserByExternalID(ctx, "invalid")
	if err != authplugin.ErrNotFound {
		t.Errorf("GetUserByExternalID(invalid) should return ErrNotFound")
	}
}

func TestNativeUserManager_CreateUser_Validation(t *testing.T) {
	m := &NativeUserManager{}
	ctx := context.Background()

	_, err := m.CreateUser(ctx, &authplugin.CreateUserRequest{
		Username: "",
		Password: "password123",
	})
	if err == nil {
		t.Errorf("CreateUser with empty username should fail")
	}

	_, err = m.CreateUser(ctx, &authplugin.CreateUserRequest{
		Username: "testuser",
		Password: "short",
	})
	if err == nil {
		t.Errorf("CreateUser with short password should fail")
	}
}
