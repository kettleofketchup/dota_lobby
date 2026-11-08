package gc

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/kettleofketchup/dota_lobby/pkg/config"
)

// Every secret in Credentials, one-time Steam Guard codes included, must stay
// out of fmt and slog output.
func TestCredentialsRedacted(t *testing.T) {
	secrets := []string{"pw-s3cret", "tok-s3cret", "TOTP5", "EMAIL9"}
	c := Credentials{
		Username:      "bot_a",
		Password:      config.Secret(secrets[0]),
		AccessToken:   config.Secret(secrets[1]),
		TwoFactorCode: config.Secret(secrets[2]),
		AuthCode:      config.Secret(secrets[3]),
		DeviceName:    "dota_lobby",
	}
	render := map[string]func() string{
		"%v":         func() string { return fmt.Sprintf("%v", c) },
		"%+v":        func() string { return fmt.Sprintf("%+v", c) },
		"%#v":        func() string { return fmt.Sprintf("%#v", c) },
		"%v pointer": func() string { return fmt.Sprintf("%v", &c) },
		"slog text": func() string {
			var b bytes.Buffer
			slog.New(slog.NewTextHandler(&b, nil)).Info("creds", "c", c)
			return b.String()
		},
		"slog json": func() string {
			var b bytes.Buffer
			slog.New(slog.NewJSONHandler(&b, nil)).Info("creds", "c", c)
			return b.String()
		},
	}
	for name, f := range render {
		t.Run(name, func(t *testing.T) {
			out := f()
			for _, s := range secrets {
				if strings.Contains(out, s) {
					t.Errorf("output leaks %q: %s", s, out)
				}
			}
			if !strings.Contains(out, "bot_a") {
				t.Errorf("output lost the username: %s", out)
			}
		})
	}
}
