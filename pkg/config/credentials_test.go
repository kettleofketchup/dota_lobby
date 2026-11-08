package config

import (
	"strings"
	"testing"
)

func TestReadCredentialFileAliasConflict(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string // error substring; empty means success
	}{
		{"same value both spellings", "STEAM_PASS: hunter2\npassword: hunter2\n", ""},
		{"conflicting password", "STEAM_PASS: hunter2\npassword: other-secret\n", "keys STEAM_PASS and password"},
		{"conflicting user", "STEAM_USER: a\nusername: b\n", "keys STEAM_USER and username"},
		{"empty alias does not conflict", "STEAM_PASS: hunter2\npassword: \"\"\n", ""},
		{"conflicting dotenv", "STEAM_REFRESH_TOKEN=aaa\nrefresh_token=bbb\n", "keys STEAM_REFRESH_TOKEN and refresh_token"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := write(t, t.TempDir(), "bot.yaml", tc.content)
			_, err := readCredentialFile(p)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
			for _, secret := range []string{"hunter2", "other-secret", "aaa", "bbb"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error leaks value %q: %v", secret, err)
				}
			}
		})
	}
}
