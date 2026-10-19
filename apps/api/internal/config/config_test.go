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

func TestFromLookupDefaults(t *testing.T) {
	c, err := FromLookup(lookupFrom(map[string]string{
		"MAKET_APP_HOST":       "App.Kotakserver.my.id",
		"MAKET_PREVIEW_DOMAIN": "cloudshare.my.id",
	}))
	if err != nil {
		t.Fatal(err)
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
}

func TestFromLookupReportsAllProblems(t *testing.T) {
	_, err := FromLookup(lookupFrom(map[string]string{
		"MAKET_PUBLIC_SCHEME": "ftp",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"MAKET_APP_HOST", "MAKET_PREVIEW_DOMAIN", "MAKET_PUBLIC_SCHEME"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestFromLookupRejectsAppUnderPreviewDomain(t *testing.T) {
	_, err := FromLookup(lookupFrom(map[string]string{
		"MAKET_APP_HOST":       "app.cloudshare.my.id",
		"MAKET_PREVIEW_DOMAIN": "cloudshare.my.id",
	}))
	if err == nil {
		t.Fatal("expected error for app host under preview domain")
	}
}
