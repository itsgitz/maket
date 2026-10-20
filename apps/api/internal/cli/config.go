package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// defaultAPIURL is the API used when neither the environment nor the config
// file names one.
const defaultAPIURL = "https://app.kotakserver.my.id"

// configFile is the saved sign-in, config.json in the config directory.
type configFile struct {
	APIURL string `json:"api_url,omitempty"`
	APIKey string `json:"api_key,omitempty"`
}

// DefaultConfigDir is the config directory of this machine:
// $XDG_CONFIG_HOME/maket, else %AppData%\maket on Windows and
// ~/.config/maket everywhere else.
func DefaultConfigDir() (string, error) {
	return configDir(os.Getenv, runtime.GOOS, os.UserHomeDir)
}

func configDir(getenv func(string) string, goos string, home func() (string, error)) (string, error) {
	if dir := getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "maket"), nil
	}
	if goos == "windows" {
		dir := getenv("AppData")
		if dir == "" {
			return "", errors.New("%AppData% is not set")
		}
		return filepath.Join(dir, "maket"), nil
	}
	h, err := home()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".config", "maket"), nil
}

// configPath returns the path of config.json.
func (r *runner) configPath() (string, error) {
	dir, err := r.env.ConfigDir()
	if err != nil {
		return "", localError("find the config directory: %v", err)
	}
	return filepath.Join(dir, "config.json"), nil
}

// readConfig reads the config file at path. A missing file is an empty
// config.
func readConfig(path string) (configFile, error) {
	var c configFile
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, localError("read config: %v", err)
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, localError("read config %s: %v", path, err)
	}
	return c, nil
}

// writeConfig replaces the config file at path atomically: the directory is
// made private (0700), the new contents go to a 0600 temp file beside it,
// which is then renamed over the old file.
func writeConfig(path string, c configFile) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return localError("write config: %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return localError("write config: %v", err)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return localError("write config: %v", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json") // created 0600
	if err != nil {
		return localError("write config: %v", err)
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	_, err = tmp.Write(append(b, '\n'))
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		return localError("write config: %v", err)
	}
	return nil
}

// settings are the API address and key in effect.
type settings struct {
	URL string
	Key string // empty: not signed in
}

// resolveSettings combines, field by field, MAKET_API_URL and MAKET_API_KEY
// with the config file. urlFlag, when set, wins over both. The config file is
// read only when the environment leaves a field unset.
func (r *runner) resolveSettings(urlFlag string) (settings, error) {
	s := settings{URL: urlFlag, Key: strings.TrimSpace(r.env.Getenv("MAKET_API_KEY"))}
	if s.URL == "" {
		s.URL = strings.TrimSpace(r.env.Getenv("MAKET_API_URL"))
	}
	if s.URL == "" || s.Key == "" {
		path, err := r.configPath()
		if err != nil {
			return s, err
		}
		c, err := readConfig(path)
		if err != nil {
			return s, err
		}
		if s.URL == "" {
			s.URL = c.APIURL
		}
		if s.Key == "" {
			s.Key = c.APIKey
		}
	}
	if s.URL == "" {
		s.URL = defaultAPIURL
	}
	u, err := normalizeURL(s.URL)
	if err != nil {
		return s, err
	}
	s.URL = u
	return s, nil
}

// requireKey returns a not-signed-in error when s has no API Key.
func requireKey(s settings) error {
	if s.Key == "" {
		return newError(exitAuth, codeNotSignedIn, "not signed in: run `maket login` or set MAKET_API_KEY")
	}
	return nil
}

// normalizeURL checks that s is an http(s) URL and trims trailing slashes.
func normalizeURL(s string) (string, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", usageError("invalid API URL %q: it must start with https:// or http://", s)
	}
	return s, nil
}

// sendsKeyInClear reports whether apiURL is plain http to a host other than
// this machine, so the API Key would cross the network unencrypted.
func sendsKeyInClear(apiURL string) bool {
	u, err := url.Parse(apiURL)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && (ip.Equal(net.IPv4(127, 0, 0, 1)) || ip.Equal(net.IPv6loopback)) {
		return false
	}
	return true
}

// warnIfInsecure prints, once, a warning when the API Key would be sent
// unencrypted.
func (r *runner) warnIfInsecure(apiURL string) {
	if r.warned || !sendsKeyInClear(apiURL) {
		return
	}
	r.warned = true
	fmt.Fprintf(r.env.Stderr, "maket: warning: %s is plain http, so the API Key is sent unencrypted\n", apiURL)
}
