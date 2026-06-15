package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchACCWorkTypes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != accWorkTypesPath {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(accWorkTypesResponse{
			OK: true,
			WorkTypes: []accWorkTypePayload{{
				Key: "general_chat", Label: "通用对话", Category: "通用",
				L1TaskType: "chat", DefaultProfile: "smart",
			}},
		})
	}))
	defer srv.Close()

	items, err := fetchACCWorkTypes(context.Background(), accSyncConfig{
		BaseURL: srv.URL, ServiceToken: "test-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Key != "general_chat" {
		t.Fatalf("unexpected items: %+v", items)
	}
}

func TestFetchACCWorkTypesNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	_, err := fetchACCWorkTypes(context.Background(), accSyncConfig{
		BaseURL: srv.URL, ServiceToken: "test-token",
	})
	if err == nil {
		t.Fatal("expected error for 404")
	}
}
