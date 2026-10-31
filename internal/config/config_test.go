package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolate(t *testing.T) string {
	t.Helper()
	t.Setenv(KeyVar, "")
	t.Setenv("PIKA_API_URL", "")
	t.Setenv("PIKA_DOCS_URL", "")
	t.Setenv("PIKACTL_DATA", "")
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}

func TestKeyPrecedence(t *testing.T) {
	dir := isolate(t)
	path := filepath.Join(dir, "config.yaml")
	if err := Save(path, File{APIKey: "from-config"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Resolve(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Key != "from-config" {
		t.Errorf("config: key = %q", cfg.Key)
	}

	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(KeyVar+"=from-dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg, _ = Resolve(path); cfg.Key != "from-dotenv" {
		t.Errorf(".env: key = %q", cfg.Key)
	}

	t.Setenv(KeyVar, "from-env")
	if cfg, _ = Resolve(path); cfg.Key != "from-env" || !strings.Contains(cfg.KeySource, "environment") {
		t.Errorf("env: key = %q from %q", cfg.Key, cfg.KeySource)
	}
}

func TestDotEnvIsFoundFromASubdirectory(t *testing.T) {
	dir := isolate(t)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(KeyVar+"=up-here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	cfg, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Key != "up-here" {
		t.Errorf("key = %q", cfg.Key)
	}
}

func TestDotEnvMayBeFreeFormNotes(t *testing.T) {
	dir := isolate(t)
	notes := "server notes: root password is in the vault\nDB=postgres://x\n\n" +
		"= stray\nexport " + KeyVar + "=\"quoted-key\"\nanything else: at all\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(notes), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Key != "quoted-key" {
		t.Errorf("key = %q", cfg.Key)
	}
}

func TestDotEnvOnlySuppliesTheKey(t *testing.T) {
	dir := isolate(t)
	// A .env in a cloned repo must not be able to send requests elsewhere.
	content := "PIKA_API_URL=https://evil.example\nPIKACTL_DATA=/tmp/evil\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != DefaultBaseURL {
		t.Errorf("base URL = %q", cfg.BaseURL)
	}
	if cfg.DataDir == "/tmp/evil" {
		t.Error("data dir taken from .env")
	}
}

func TestRequireKey(t *testing.T) {
	isolate(t)
	cfg, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.RequireKey(); err == nil || !strings.HasPrefix(err.Error(), "no API key") {
		t.Errorf("err = %v", err)
	}
}

func TestSavedFileIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	if err := Save(path, File{APIKey: "k"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config mode = %o, want 600", perm)
	}
}

func TestMissingConfigIsFine(t *testing.T) {
	isolate(t)
	if _, err := Resolve(filepath.Join(t.TempDir(), "absent.yaml")); err != nil {
		t.Error(err)
	}
}

func TestBadConfigIsReported(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("{not yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(path); err == nil {
		t.Error("expected an error")
	}
}
