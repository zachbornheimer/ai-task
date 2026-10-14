package proof

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/zachbornheimer/ai-task/internal/fault"
)

const (
	httpBodyLimit   = 1 << 20 // bytes of body the body= regex sees
	httpReasonBytes = 200     // bytes of body quoted in a failure reason
)

func init() { registerBuiltin(func() Preset { return newHTTPOKPreset(nil) }) }

// httpOKPreset proves a URL answers a GET: http_ok: URL [status=CODE body=REGEX].
type httpOKPreset struct{ client *http.Client }

var _ Evaluator = httpOKPreset{}

// newHTTPOKPreset uses client, or one whose timeout is the proof budget.
func newHTTPOKPreset(client *http.Client) httpOKPreset {
	if client == nil {
		client = &http.Client{Timeout: ProofBudget}
	}
	return httpOKPreset{client: client}
}

func (httpOKPreset) Name() string      { return "http_ok" }
func (httpOKPreset) Options() []string { return []string{"status", "body"} }
func (httpOKPreset) DefaultSize() Size { return SizeSmall }

// Argv is never used: http_ok runs in-process through Evaluate.
func (httpOKPreset) Argv(ProofSpec, Env) ([]string, error) {
	return nil, fault.New(fault.CodeInvalidInput, "http_ok is evaluated in-process, not as a command")
}

func (p httpOKPreset) Evaluate(spec ProofSpec, _ string) (bool, string, error) {
	u, err := url.Parse(spec.Subject)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false, "", fault.New(fault.CodeInvalidInput, "http_ok needs an http or https URL, got %q", spec.Subject)
	}
	want, err := wantedStatus(spec.Options["status"])
	if err != nil {
		return false, "", err
	}
	var bodyRE *regexp.Regexp
	if expr, ok := spec.Options["body"]; ok {
		if bodyRE, err = regexp.Compile(expr); err != nil {
			return false, "", fault.Wrap(err, fault.CodeInvalidInput, "http_ok body=%q", expr)
		}
	}
	resp, err := p.client.Get(u.String())
	if err != nil {
		return false, requestFailure(err), nil
	}
	defer func() { _ = resp.Body.Close() }() // response fully consumed or abandoned; nothing to recover
	body, _ := io.ReadAll(io.LimitReader(resp.Body, httpBodyLimit))
	statusOK := resp.StatusCode/100 == 2
	if want != 0 {
		statusOK = resp.StatusCode == want
	}
	if statusOK && (bodyRE == nil || bodyRE.Match(body)) {
		return true, fmt.Sprintf("GET %s -> %d", u, resp.StatusCode), nil
	}
	return false, fmt.Sprintf("GET %s -> %d, body: %.*q", u, resp.StatusCode, httpReasonBytes, body), nil
}

// wantedStatus parses status=CODE; 0 means any 2xx.
func wantedStatus(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	code, err := strconv.Atoi(s)
	if err != nil || code < 100 || code > 599 {
		return 0, fault.New(fault.CodeInvalidInput, "http_ok status=%q is not an HTTP status code", s)
	}
	return code, nil
}

func requestFailure(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "request timeout: " + err.Error()
	}
	return "request failed: " + err.Error()
}
