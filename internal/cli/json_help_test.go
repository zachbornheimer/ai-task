package cli

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestJSONHelpEnvelope(t *testing.T) {
	t.Parallel()

	type envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	decodeOne := func(t *testing.T, stdout string) envelope {
		t.Helper()
		dec := json.NewDecoder(strings.NewReader(stdout))
		var got envelope
		if err := dec.Decode(&got); err != nil {
			t.Fatalf("stdout is not a JSON envelope: %v\n%s", err, stdout)
		}
		var extra json.RawMessage
		if err := dec.Decode(&extra); err != io.EOF {
			t.Fatalf("stdout must be exactly one JSON envelope, extra %q err=%v\n%s", extra, err, stdout)
		}
		return got
	}

	t.Run("json help", func(t *testing.T) {
		code, out, errOut := runAt(t, "--json", "help")
		if code != ExitOK {
			t.Fatalf("atask --json help: exit %d stderr %q stdout %q", code, errOut, out)
		}
		if strings.TrimSpace(errOut) != "" {
			t.Fatalf("atask --json help wrote stderr: %q", errOut)
		}
		got := decodeOne(t, out)
		if !got.OK || len(got.Result) == 0 || got.Error != nil {
			t.Fatalf("atask --json help envelope: %+v", got)
		}
	})

	t.Run("unknown command with leading json", func(t *testing.T) {
		code, out, errOut := runAt(t, "--json", "nosuch")
		if code != ExitUsage {
			t.Fatalf("atask --json nosuch: exit %d stderr %q stdout %q", code, errOut, out)
		}
		if strings.TrimSpace(errOut) != "" {
			t.Fatalf("atask --json nosuch wrote stderr: %q", errOut)
		}
		got := decodeOne(t, out)
		if got.OK || got.Error == nil || got.Error.Code != "INVALID_INPUT" {
			t.Fatalf("atask --json nosuch envelope: %+v", got)
		}
		if !strings.Contains(got.Error.Message, `unknown command "nosuch"`) {
			t.Fatalf("atask --json nosuch message: %q", got.Error.Message)
		}
	})

	t.Run("unknown command with trailing json", func(t *testing.T) {
		code, out, errOut := runAt(t, "nosuch", "--json")
		if code != ExitUsage {
			t.Fatalf("atask nosuch --json: exit %d stderr %q stdout %q", code, errOut, out)
		}
		if strings.TrimSpace(errOut) != "" {
			t.Fatalf("atask nosuch --json wrote stderr: %q", errOut)
		}
		got := decodeOne(t, out)
		if got.OK || got.Error == nil || got.Error.Code != "INVALID_INPUT" {
			t.Fatalf("atask nosuch --json envelope: %+v", got)
		}
		if !strings.Contains(got.Error.Message, `unknown command "nosuch"`) {
			t.Fatalf("atask nosuch --json message: %q", got.Error.Message)
		}
	})
}
