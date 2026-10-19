package preview

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSlugFromHost(t *testing.T) {
	const domain = "cloudshare.my.id"
	tests := []struct {
		host, want string
		ok         bool
	}{
		{"ab12cd34.cloudshare.my.id", "ab12cd34", true},
		{"AB12CD34.cloudshare.my.id", "ab12cd34", true},
		{"cloudshare.my.id", "", false},
		{"ab12cd3.cloudshare.my.id", "", false},   // too short
		{"ab12cd345.cloudshare.my.id", "", false}, // too long
		{"ab12-d34.cloudshare.my.id", "", false},
		{"x.ab12cd34.cloudshare.my.id", "", false},
		{"ab12cd34.cloudshare.my.id.evil.com", "", false},
		{"ab12cd34.notcloudshare.my.id", "", false},
	}
	for _, tt := range tests {
		got, ok := SlugFromHost(tt.host, domain)
		if got != tt.want || ok != tt.ok {
			t.Errorf("SlugFromHost(%q) = %q, %v; want %q, %v", tt.host, got, ok, tt.want, tt.ok)
		}
	}
}

func TestServePreviewSetsHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler{}.ServePreview(rec, httptest.NewRequest(http.MethodGet, "/", nil), "ab12cd34")
	for k, v := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Robots-Tag":           "noindex, nofollow",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	} {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("preview responses must never set cookies")
	}
}
