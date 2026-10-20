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
		"MAKET_STORAGE_DRIVER": "gcs",
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

func TestFromLookupLimits(t *testing.T) {
	c, err := FromLookup(lookupFrom(valid(map[string]string{"MAKET_MAX_FILES": "50", "MAKET_USER_QUOTA_MB": "2"})))
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxUploadBytes != 20<<20 || c.MaxExtractedBytes != 100<<20 || c.MaxFiles != 50 || c.UserQuotaBytes != 2<<20 {
		t.Errorf("limits = %d %d %d %d", c.MaxUploadBytes, c.MaxExtractedBytes, c.MaxFiles, c.UserQuotaBytes)
	}
	if c.CloudflarePurge() {
		t.Error("purge should be off by default")
	}
	for _, env := range []map[string]string{
		{"MAKET_MAX_FILES": "0"},
		{"MAKET_MAX_UPLOAD_MB": "ten"},
		{"MAKET_CF_API_TOKEN": "t"},
		{"MAKET_CF_ZONE_ID": "z"},
	} {
		if _, err := FromLookup(lookupFrom(valid(env))); err == nil {
			t.Errorf("%v: expected error", env)
		}
	}
}

func TestFromLookupStorageDriver(t *testing.T) {
	s3Env := map[string]string{
		"MAKET_STORAGE_DRIVER":       "s3",
		"MAKET_S3_ENDPOINT":          "https://acct.r2.cloudflarestorage.com",
		"MAKET_S3_BUCKET":            "maket",
		"MAKET_S3_ACCESS_KEY_ID":     "key",
		"MAKET_S3_SECRET_ACCESS_KEY": "secret",
	}
	with := func(kv ...string) map[string]string {
		env := valid(nil)
		for k, v := range s3Env {
			env[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			if kv[i+1] == "" {
				delete(env, kv[i])
			} else {
				env[kv[i]] = kv[i+1]
			}
		}
		return env
	}
	tests := []struct {
		name   string
		env    map[string]string
		want   string // substring of the error; "" means no error
		region string
		driver string
	}{
		{name: "local is the default", env: valid(nil), driver: "local"},
		{name: "s3 with defaults", env: with(), driver: "s3", region: "auto"},
		{name: "s3 explicit region", env: with("MAKET_S3_REGION", "us-east-1"), driver: "s3", region: "us-east-1"},
		{name: "s3 plain http for MinIO", env: with("MAKET_S3_ENDPOINT", "http://127.0.0.1:9000"), driver: "s3", region: "auto"},
		{name: "s3 missing endpoint", env: with("MAKET_S3_ENDPOINT", ""), want: "MAKET_S3_ENDPOINT is required"},
		{name: "s3 endpoint without scheme", env: with("MAKET_S3_ENDPOINT", "acct.r2.cloudflarestorage.com"), want: "MAKET_S3_ENDPOINT must be an http or https URL"},
		{name: "s3 endpoint with ftp scheme", env: with("MAKET_S3_ENDPOINT", "ftp://host"), want: "MAKET_S3_ENDPOINT must be an http or https URL"},
		{name: "s3 missing bucket", env: with("MAKET_S3_BUCKET", ""), want: "MAKET_S3_BUCKET is required"},
		{name: "s3 missing access key", env: with("MAKET_S3_ACCESS_KEY_ID", ""), want: "MAKET_S3_ACCESS_KEY_ID is required"},
		{name: "s3 missing secret", env: with("MAKET_S3_SECRET_ACCESS_KEY", ""), want: "MAKET_S3_SECRET_ACCESS_KEY is required"},
		{name: "unknown driver", env: with("MAKET_STORAGE_DRIVER", "gcs"), want: `MAKET_STORAGE_DRIVER must be "local" or "s3", got "gcs"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := FromLookup(lookupFrom(tt.env))
			if tt.want != "" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.StorageDriver != tt.driver {
				t.Errorf("driver = %q, want %q", c.StorageDriver, tt.driver)
			}
			if tt.driver == "s3" && c.S3Region != tt.region {
				t.Errorf("region = %q, want %q", c.S3Region, tt.region)
			}
		})
	}
}

func TestFromLookupS3ReportsAllMissing(t *testing.T) {
	_, err := FromLookup(lookupFrom(valid(map[string]string{"MAKET_STORAGE_DRIVER": "s3"})))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"MAKET_S3_ENDPOINT", "MAKET_S3_BUCKET", "MAKET_S3_ACCESS_KEY_ID", "MAKET_S3_SECRET_ACCESS_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}
