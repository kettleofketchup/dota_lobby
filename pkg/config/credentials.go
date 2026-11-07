package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// credentialFileKeys maps the keys of a per-bot credentials file to the
// secrets::bots::<bot> fields. The STEAM_* names are the ones in
// secrets/bots/<bot>.sops.yaml (just/secrets.just); the lowercase names match
// secrets.yaml so either style works.
var credentialFileKeys = map[string]string{
	"STEAM_USER":          "username",
	"STEAM_PASS":          "password",
	"STEAM_SHARED_SECRET": "shared_secret",
	"STEAM_REFRESH_TOKEN": "refresh_token",
	"STEAM_DEVICE_NAME":   "device_name",
	"username":            "username",
	"password":            "password",
	"shared_secret":       "shared_secret",
	"refresh_token":       "refresh_token",
	"device_name":         "device_name",
}

// secretFileKeys are the credentials file keys that must be strings.
var secretFileKeys = map[string]bool{
	"STEAM_PASS": true, "STEAM_SHARED_SECRET": true, "STEAM_REFRESH_TOKEN": true,
	"password": true, "shared_secret": true, "refresh_token": true,
}

// credentialExts are the decrypted formats accepted in credentials_dir.
var credentialExts = []string{".yaml", ".yml", ".json", ".env"}

// credentialFile is one decrypted bot credentials file, keyed by the
// secrets::bots::<bot> field names.
type credentialFile struct {
	path   string
	fields map[string]string
}

// readCredentialFile reads a decrypted credentials file. The format is YAML
// or JSON (what `sops decrypt` and `sops exec-file` produce for a .sops.yaml)
// or dotenv (`--output-type dotenv`). Unknown keys are ignored so the file can
// carry notes for other tools; empty values are dropped so they do not mask a
// lower layer.
func readCredentialFile(path string) (credentialFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return credentialFile{}, fmt.Errorf("read credentials file: %w", err)
	}
	raw, err := parseCredentials(data)
	if err != nil {
		return credentialFile{}, fmt.Errorf("parse credentials file %s: %w", path, err)
	}
	fields := map[string]string{}
	for k, v := range raw {
		if field, ok := credentialFileKeys[k]; ok && v != "" {
			fields[field] = v
		}
	}
	return credentialFile{path: path, fields: fields}, nil
}

func parseCredentials(data []byte) (map[string]string, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err == nil && doc != nil {
		if _, ok := doc["sops"]; ok {
			return nil, errors.New("file is still sops-encrypted; decrypt it first (sops decrypt or sops exec-file)")
		}
		out := make(map[string]string, len(doc))
		for k, v := range doc {
			switch v := v.(type) {
			case nil:
			case string:
				out[k] = v
			case bool, int, int64, uint64, float64:
				if secretFileKeys[k] {
					// YAML turns an unquoted 0x1F into 31; a rewritten
					// password fails every logon. The value is not echoed.
					return nil, fmt.Errorf("key %s: quote the value (YAML read it as a %T)", k, v)
				}
				out[k] = fmt.Sprint(v)
			default:
				return nil, fmt.Errorf("key %s: want a string, got %T", k, v)
			}
		}
		return out, nil
	}
	return parseDotenv(data)
}

// parseDotenv reads KEY=VALUE lines as written by sops --output-type dotenv,
// plus the usual comments, blank lines, "export " prefixes and quoting.
func parseDotenv(data []byte) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: not YAML and not KEY=VALUE", n)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch {
		case len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"':
			uq, err := strconv.Unquote(v)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", n, err)
			}
			v = uq
		case len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'':
			v = v[1 : len(v)-1]
		default:
			// sops escapes newlines in unquoted dotenv values.
			v = strings.ReplaceAll(v, `\n`, "\n")
		}
		out[k] = v
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	return out, nil
}

// scanCredentialsDir maps bot name to credentials file path. "bot-a.yaml"
// and "bot-a.sops.yaml" (decrypted in place under its original name) both
// name the bot "bot-a".
func scanCredentialsDir(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read credentials_dir: %w", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		ext := filepath.Ext(e.Name())
		if !slices.Contains(credentialExts, ext) {
			continue
		}
		name := botNameFromFile(e.Name())
		key := strings.ToLower(name)
		if prev, dup := out[key]; dup {
			return nil, fmt.Errorf("credentials_dir: %s and %s both name bot %q", filepath.Base(prev), e.Name(), name)
		}
		out[key] = filepath.Join(dir, e.Name())
	}
	return out, nil
}

// botNameFromFile is the bot name a credentials file stands for.
func botNameFromFile(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(strings.TrimSuffix(base, filepath.Ext(base)), ".sops")
}
