package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) LookupFunc {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestDefaults(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":21114" {
		t.Errorf("listen = %q", cfg.Listen)
	}
	if cfg.DBPath != filepath.Join("/data", "panel.db") {
		t.Errorf("db path = %q", cfg.DBPath)
	}
	if cfg.Auth.TokenLifetime != 4380*time.Hour || cfg.Auth.JWTLifetime != 4380*time.Hour {
		t.Errorf("lifetimes = %v / %v", cfg.Auth.TokenLifetime, cfg.Auth.JWTLifetime)
	}
	if got := cfg.HBBS.AdminAddr(); got != "127.0.0.1:21115" {
		t.Errorf("hbbs admin = %q", got)
	}
	if got := cfg.HBBS.RelayAdminAddr(); got != "127.0.0.1:21117" {
		t.Errorf("hbbr admin = %q", got)
	}
	if n := len(cfg.HTTP.TrustedProxyPrefixes()); n != 2 {
		t.Errorf("trusted proxies = %d", n)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "rdpanel.yaml")
	yaml := "listen: 127.0.0.1:9000\npublic_url: https://example.test/\ndata_dir: " + dir + "\n" +
		"auth:\n  token_lifetime: 24h\nlog:\n  level: debug\n"
	if err := os.WriteFile(file, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(env(map[string]string{
		"RDPANEL_CONFIG":          file,
		"RDPANEL_LISTEN":          "127.0.0.1:31114",
		"RDPANEL_TRUSTED_PROXIES": "10.0.0.0/8, 192.168.1.1",
		"RDPANEL_REAL_IP_HEADER":  "cf-connecting-ip",
		"RUSTDESK_API_JWT_KEY":    "  not-trimmed-inside  ",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:31114" {
		t.Errorf("env must override file: %q", cfg.Listen)
	}
	if cfg.PublicURL != "https://example.test" || !cfg.HTTPS() {
		t.Errorf("public url = %q", cfg.PublicURL)
	}
	if cfg.Auth.TokenLifetime != 24*time.Hour || cfg.Auth.JWTLifetime != 24*time.Hour {
		t.Errorf("token lifetime = %v", cfg.Auth.TokenLifetime)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("level = %q", cfg.Log.Level)
	}
	if cfg.HTTP.RealIPHeader != "Cf-Connecting-Ip" {
		t.Errorf("header = %q", cfg.HTTP.RealIPHeader)
	}
	p := cfg.HTTP.TrustedProxyPrefixes()
	if len(p) != 2 || p[1].String() != "192.168.1.1/32" {
		t.Errorf("trusted = %v", p)
	}
	if cfg.Auth.JWTKey.Reveal() != "  not-trimmed-inside  " {
		t.Errorf("jwt key = %q", cfg.Auth.JWTKey.Reveal())
	}
}

func TestUnknownYAMLFieldRejected(t *testing.T) {
	file := filepath.Join(t.TempDir(), "c.yaml")
	_ = os.WriteFile(file, []byte("lisen: :1\n"), 0o600)
	if _, err := Load(env(map[string]string{"RDPANEL_CONFIG": file})); err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestEmptyYAMLFileOK(t *testing.T) {
	file := filepath.Join(t.TempDir(), "c.yaml")
	_ = os.WriteFile(file, nil, 0o600)
	if _, err := Load(env(map[string]string{"RDPANEL_CONFIG": file})); err != nil {
		t.Fatal(err)
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]map[string]string{
		"bad listen":     {"RDPANEL_LISTEN": "21114"},
		"bad url scheme": {"RDPANEL_PUBLIC_URL": "ftp://x"},
		"url query":      {"RDPANEL_PUBLIC_URL": "https://x/?a=1"},
		"bad duration":   {"RDPANEL_TOKEN_LIFETIME": "forever"},
		"zero lifetime":  {"RDPANEL_TOKEN_LIFETIME": "0s"},
		"idle > abs":     {"RDPANEL_SESSION_IDLE": "200h"},
		"bad proxy":      {"RDPANEL_TRUSTED_PROXIES": "not-an-ip"},
		"bad header":     {"RDPANEL_REAL_IP_HEADER": "X-Client-IP"},
		"bad hbbs":       {"RDPANEL_HBBS_ID_SERVER": "localhost"},
		"bad level":      {"RDPANEL_LOG_LEVEL": "trace"},
		"bad secret":     {"RDPANEL_SECRET_KEY": base64.StdEncoding.EncodeToString(make([]byte, 16))},
	}
	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(env(e)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestSecretsRedacted(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg, err := Load(env(map[string]string{
		"RUSTDESK_API_JWT_KEY": "super-secret-jwt",
		"RDPANEL_SECRET_KEY":   key,
	}))
	if err != nil {
		t.Fatal(err)
	}
	out, err := cfg.Redacted()
	if err != nil {
		t.Fatal(err)
	}
	js, _ := json.Marshal(cfg)
	for _, s := range []string{string(out), string(js), fmt.Sprintf("%v %+v %#v", cfg, cfg, cfg)} {
		if strings.Contains(s, "super-secret-jwt") || strings.Contains(s, key) {
			t.Fatalf("secret leaked: %s", s)
		}
	}
	if !strings.Contains(string(out), "[redacted]") {
		t.Errorf("expected redaction marker in:\n%s", out)
	}
	if !strings.Contains(string(out), "token_lifetime: 4380h0m0s") {
		t.Errorf("durations should print human-readable:\n%s", out)
	}
}

func TestEnvNames(t *testing.T) {
	names := strings.Join(EnvNames(), ",")
	for _, want := range []string{"RDPANEL_CONFIG", "RUSTDESK_API_JWT_KEY", "RDPANEL_LISTEN"} {
		if !strings.Contains(names, want) {
			t.Errorf("missing %s", want)
		}
	}
}
