package cfpurge_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/itsgitz/maket/apps/api/internal/cfpurge"
)

const (
	token = "s3cr3t-token-value"
	zone  = "zone123"
	host  = "ab12cd34.cloudshare.my.id"
)

func newClient(t *testing.T, h http.HandlerFunc) *cfpurge.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return cfpurge.New(token, zone, cfpurge.WithBaseURL(srv.URL+"/"), cfpurge.WithHTTPClient(srv.Client()))
}

func TestPurgeRequest(t *testing.T) {
	var calls int
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/zones/"+zone+"/purge_cache" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatalf("body %s: %v", b, err)
		}
		if len(body) != 1 {
			t.Errorf("body has extra fields: %s", b)
		}
		hosts, _ := body["hosts"].([]any)
		if len(hosts) != 1 || hosts[0] != host {
			t.Errorf("hosts = %v", body["hosts"])
		}
		io.WriteString(w, `{"success":true,"errors":[],"messages":[],"result":{"id":"`+zone+`"}}`)
	})
	paths := []string{"/", "/index.html", "/assets/app.js", "/docs/", "/index.html"}
	if err := c.Purge(context.Background(), host, paths); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestPurgeErrors(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		wantSub   []string
		rateLimit bool
	}{
		{
			name:    "success false",
			status:  200,
			body:    `{"success":false,"errors":[{"code":1012,"message":"Request must contain one of \"purge_everything\", \"files\", \"tags\", \"hosts\" or \"prefixes\""}]}`,
			wantSub: []string{"1012", "Request must contain"},
		},
		{
			name:    "non-2xx with envelope",
			status:  403,
			body:    `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`,
			wantSub: []string{"403", "10000 Authentication error"},
		},
		{
			name:    "non-2xx without envelope",
			status:  502,
			body:    `<html>bad gateway</html>`,
			wantSub: []string{"502"},
		},
		{
			name:      "rate limited",
			status:    429,
			body:      `{"success":false,"errors":[{"code":1134,"message":"Unable to purge, rate limit reached."}]}`,
			wantSub:   []string{"429", "rate limit reached"},
			rateLimit: true,
		},
		{
			name:    "2xx with invalid json",
			status:  200,
			body:    `not json`,
			wantSub: []string{"decode response"},
		},
		{
			name:    "multiple errors",
			status:  400,
			body:    `{"success":false,"errors":[{"code":1,"message":"first"},{"code":2,"message":"second"}]}`,
			wantSub: []string{"1 first", "2 second"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			})
			err := c.Purge(context.Background(), host, nil)
			if err == nil {
				t.Fatal("want error")
			}
			for _, s := range tt.wantSub {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q does not contain %q", err, s)
				}
			}
			if got := errors.Is(err, cfpurge.ErrRateLimited); got != tt.rateLimit {
				t.Errorf("errors.Is(ErrRateLimited) = %v, want %v", got, tt.rateLimit)
			}
			if strings.Contains(err.Error(), token) {
				t.Errorf("error leaks token: %q", err)
			}
		})
	}
}

func TestPurgeInvalidHost(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request expected")
	})
	for _, h := range []string{"", "https://" + host, host + "/x", "a b"} {
		if err := c.Purge(context.Background(), h, nil); err == nil {
			t.Errorf("Purge(%q) = nil, want error", h)
		}
	}
}

func TestPurgeContextCanceled(t *testing.T) {
	release := make(chan struct{})
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := c.Purge(ctx, host, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("error leaks token: %q", err)
	}
}

func TestPurgeAlreadyCanceled(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request expected")
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Purge(ctx, host, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want Canceled", err)
	}
}

func TestPurgeConnectionError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	c := cfpurge.New(token, zone, cfpurge.WithBaseURL(u))
	err := c.Purge(context.Background(), host, nil)
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("error leaks token: %q", err)
	}
}
