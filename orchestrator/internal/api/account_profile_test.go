package api

import (
	"context"
	"github.com/cnjack/jcloud/internal/domain"
	"net/http"
	"testing"
)

func TestAccountProfilePersistsOnlyForCurrentAccount(t *testing.T) {
	ts, st, _ := newTestServer(t)
	alice, bob := mkUser(t, st, "profile-alice"), mkUser(t, st, "profile-bob")
	token := mkSession(t, st, alice.ID)
	input := domain.AccountProfile{DisplayName: "Alice Updated", Preferences: domain.AccountPreferences{PermissionMode: "approval", Effort: "high", SendKey: "mod_enter", Language: "zh-Hans", Theme: "light"}}
	resp := do(t, http.MethodPut, ts.URL+"/api/v1/account/profile", token, input)
	if resp.StatusCode != 200 {
		t.Fatalf("save: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, http.MethodGet, ts.URL+"/api/v1/account/profile", token, nil)
	var saved domain.AccountProfile
	decode(t, resp, &saved)
	if saved != input {
		t.Fatalf("saved = %+v", saved)
	}
	user, _ := st.GetUser(context.Background(), alice.ID)
	if user.DisplayName != input.DisplayName {
		t.Fatal("profile name did not update auth identity")
	}
	other, _ := st.GetAccountProfile(context.Background(), bob.ID)
	if other.DisplayName == input.DisplayName || other.Preferences.Effort != "" {
		t.Fatal("cross-account mutation")
	}
}

func TestAccountProfileRejectsServiceAndInvalidDefaults(t *testing.T) {
	ts, st, _ := newTestServer(t)
	token := mkSession(t, st, mkUser(t, st, "profile-validation").ID)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		resp := do(t, method, ts.URL+"/api/v1/account/profile", consoleToken, nil)
		if resp.StatusCode != 403 {
			t.Fatalf("service %s: %d", method, resp.StatusCode)
		}
		resp.Body.Close()
	}
	for _, tc := range []struct {
		input  map[string]any
		status int
	}{
		{map[string]any{"display_name": " "}, 400},
		{map[string]any{"display_name": "Alice", "preferences": map[string]string{"permission_mode": "admin"}}, 400},
		{map[string]any{"display_name": "Alice", "preferences": map[string]string{"default_model_id": "someone-elses-model"}}, 409},
		{map[string]any{"display_name": "Alice", "user_id": "another-account"}, 400},
	} {
		resp := do(t, http.MethodPut, ts.URL+"/api/v1/account/profile", token, tc.input)
		if resp.StatusCode != tc.status {
			t.Fatalf("input %+v: %d want %d", tc.input, resp.StatusCode, tc.status)
		}
		resp.Body.Close()
	}
}
