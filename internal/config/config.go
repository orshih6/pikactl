// Package config resolves pikactl's configuration from the environment, a
// .env file, and an optional YAML config file.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// The hosts Pika serves its API and its documentation from.
const (
	DefaultBaseURL = "https://api.dev.pika.art"
	DefaultDocsURL = "https://dev.pika.art"
)

// KeyVar is the environment variable (and .env entry) holding the API key.
const KeyVar = "PIKA_API_KEY"

// Config holds the fully resolved settings pikactl runs with.
type Config struct {
	// Key is the API key. KeySource says where it came from, for `auth status`.
	Key       string
	KeySource string
	BaseURL   string
	DocsURL   string
	// DataDir holds the history, downloaded outputs and the catalog cache.
	DataDir string
	// Path is the config file the settings were read from (and `auth login`
	// writes to). It need not exist.
	Path string
}

// File mirrors the YAML config file's shape.
type File struct {
	APIKey  string `yaml:"api_key,omitempty"`
	BaseURL string `yaml:"base_url,omitempty"`
	DocsURL string `yaml:"docs_url,omitempty"`
	DataDir string `yaml:"data_dir,omitempty"`
}

// DefaultDir is where pikactl keeps its config file.
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "pikactl")
}

// DefaultDataDir is where pikactl keeps its history and outputs. They are
// data rather than config: deleting the config must never lose them.
func DefaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "pikactl")
}

// Resolve determines the effective configuration, applying precedence
// environment > .env file > config file > default. It does not require a key,
// because the catalog and quotes are public; call RequireKey before an
// authenticated request.
func Resolve(path string) (Config, error) {
	fc, err := Load(path)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		BaseURL: strings.TrimRight(firstNonEmpty(os.Getenv("PIKA_API_URL"), fc.BaseURL, DefaultBaseURL), "/"),
		DocsURL: strings.TrimRight(firstNonEmpty(os.Getenv("PIKA_DOCS_URL"), fc.DocsURL, DefaultDocsURL), "/"),
		DataDir: firstNonEmpty(os.Getenv("PIKACTL_DATA"), fc.DataDir, DefaultDataDir()),
		Path:    path,
	}
	if cfg.Key = os.Getenv(KeyVar); cfg.Key != "" {
		cfg.KeySource = "environment (" + KeyVar + ")"
		return cfg, nil
	}
	if key, from := keyFromDotEnv(); key != "" {
		cfg.Key, cfg.KeySource = key, from
		return cfg, nil
	}
	if fc.APIKey != "" {
		cfg.Key, cfg.KeySource = fc.APIKey, path
	}
	return cfg, nil
}

// RequireKey returns an error describing how to give pikactl a key if none
// was resolved.
func (c Config) RequireKey() error {
	if c.Key != "" {
		return nil
	}
	return errors.New("no API key: create one at https://dev.pika.art/keys, then run `pikactl auth login` or set " + KeyVar + " (in the environment or a .env file)")
}

// keyFromDotEnv looks for KeyVar in the nearest .env walking up from the
// working directory. Such a file can be free-form notes rather than shell
// syntax, so only the one line is read and nothing else is interpreted.
func keyFromDotEnv() (key, path string) {
	dir, err := os.Getwd()
	if err != nil {
		return "", ""
	}
	for {
		path = filepath.Join(dir, ".env")
		if key = readDotEnvKey(path); key != "" {
			return key, path
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ""
		}
		dir = parent
	}
}

func readDotEnvKey(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimPrefix(strings.TrimSpace(scanner.Text()), "export ")
		name, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(name) == KeyVar {
			return strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return ""
}

// Load reads and parses the YAML config file at path. A missing file is not
// an error; it just yields a zero-value File.
func Load(path string) (File, error) {
	var fc File
	if path == "" {
		return fc, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fc, nil
		}
		return fc, fmt.Errorf("reading config file: %w", err)
	}
	if err := yaml.Unmarshal(data, &fc); err != nil {
		return fc, fmt.Errorf("parsing config file %s: %w", path, err)
	}
	return fc, nil
}

// Save writes fc to path, readable by its owner only since it may hold the
// API key.
func Save(path string, fc File) error {
	if path == "" {
		return errors.New("no config file path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(fc)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
