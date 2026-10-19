package config

import (
	"strings"
	"testing"
)

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
}

// valid returns a complete environment, with overrides applied.
func valid(overrides map[string]string) map[string]string {
	env := map[string]string{
		"MAKET_APP_HOST":             "app.kotakserver.my.id",
		"MAKET_PREVIEW_DOMAIN":       "cloudshare.my.id",
		"MAKET_DATA_DIR":             "/data",
		"MAKET_GITHUB_CLIENT_ID":     "id",
		"MAKET_GITHUB_CLIENT_SECRET": "secret",
		"MAKET_ALLOWED_GITHUB_USERS": "anggit",
	}
	for k, v := range overrides {
		env[k] = v
	}
	return env
}

func TestFromLookupDefaults(t *testing.T) {
	c, err := FromLookup(lookupFrom(valid(map[string]string{
		"MAKET_APP_HOST":             "App.Kotakserver.my.id",
		"MAKET_ALLOWED_GITHUB_USERS": " anggit, ,Teammate1 ",
	})))
	if err != nil {
		t.Fatal(err)
	}
	if c.StorageDriver != "local" || c.DBPath() != "/data/maket.db" || c.StorageDir() != "/data/storage" {
		t.Errorf("data settings: driver %q db %q storage %q", c.StorageDriver, c.DBPath(), c.StorageDir())
	}
	if c.ListenAddr != ":8080" || c.PublicScheme != "https" {
		t.Errorf("defaults: got listen %q scheme %q", c.ListenAddr, c.PublicScheme)
	}
	if c.AppHost != "app.kotakserver.my.id" {
		t.Errorf("AppHost not lowercased: %q", c.AppHost)
	}
	if got := c.PreviewURL("ab12cd34"); got != "https://ab12cd34.cloudshare.my.id" {
		t.Errorf("PreviewURL = %q", got)
	}
	if got := c.AppOrigin(); got != "https://app.kotakserver.my.id" {
		t.Errorf("AppOrigin = %q", got)
	}
	if got := c.GitHubRedirectURL(); got != "https://app.kotakserver.my.id/api/auth/github/callback" {
		t.Errorf("GitHubRedirectURL = %q", got)
	}
	if !c.Secure() {
		t.Error("https should be Secure")
	}
	if got := strings.Join(c.AllowedGitHubUsers, "|"); got != "anggit|Teammate1" {
		t.Errorf("AllowedGitHubUsers = %q", got)
	}
}

func TestFromLookupReportsAllProblems(t *testing.T) {
	_, err := FromLookup(lookupFrom(map[string]string{
		"MAKET_PUBLIC_SCHEME":  "ftp",
		"MAKET_STORAGE_DRIVER": "s3",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"MAKET_APP_HOST", "MAKET_PREVIEW_DOMAIN", "MAKET_PUBLIC_SCHEME", "MAKET_DATA_DIR", "MAKET_STORAGE_DRIVER",
		"MAKET_GITHUB_CLIENT_ID", "MAKET_GITHUB_CLIENT_SECRET", "MAKET_ALLOWED_GITHUB_USERS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestFromLookupRejectsAppUnderPreviewDomain(t *testing.T) {
	_, err := FromLookup(lookupFrom(valid(map[string]string{
		"MAKET_APP_HOST": "app.cloudshare.my.id",
	})))
	if err == nil {
		t.Fatal("expected error for app host under preview domain")
	}
}

func TestFromLookupRejectsBlankAllowlist(t *testing.T) {
	_, err := FromLookup(lookupFrom(valid(map[string]string{
		"MAKET_ALLOWED_GITHUB_USERS": " , ,",
	})))
	if err == nil || !strings.Contains(err.Error(), "MAKET_ALLOWED_GITHUB_USERS") {
		t.Errorf("got %v, want allowlist error", err)
	}
}
