package proof

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPresetHTTPOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/slow":
			time.Sleep(300 * time.Millisecond)
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("nothing here"))
		default:
			_, _ = w.Write([]byte("hello world"))
		}
	}))
	defer srv.Close()

	cases := []struct {
		name     string
		subject  string
		opts     map[string]string
		client   *http.Client
		wantPass bool
		wantErr  bool
		reason   string
	}{
		{name: "200 passes", subject: srv.URL, wantPass: true},
		{name: "404 fails", subject: srv.URL + "/missing", reason: "404"},
		{name: "status=404 passes", subject: srv.URL + "/missing", opts: map[string]string{"status": "404"}, wantPass: true},
		{name: "body matches", subject: srv.URL, opts: map[string]string{"body": "hello.w"}, wantPass: true},
		{name: "body mismatch", subject: srv.URL, opts: map[string]string{"body": "^goodbye"}, reason: "hello world"},
		{name: "non-http rejected", subject: "ftp://example.com/x", wantErr: true},
		{name: "timeout fails", subject: srv.URL + "/slow", client: &http.Client{Timeout: 50 * time.Millisecond}, reason: "timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newHTTPOKPreset(tc.client)
			spec := ProofSpec{Preset: "http_ok", Subject: tc.subject, Options: tc.opts}
			passed, reason, err := p.Evaluate(spec, "")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if passed != tc.wantPass {
				t.Fatalf("passed = %v (%s), want %v", passed, reason, tc.wantPass)
			}
			if !strings.Contains(strings.ToLower(reason), tc.reason) {
				t.Fatalf("reason %q lacks %q", reason, tc.reason)
			}
		})
	}
}
