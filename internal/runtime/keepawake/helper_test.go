package keepawake

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelperHandshake(t *testing.T) {
	token := strings.Repeat("a", 64)
	for _, tc := range []struct {
		input  string
		code   int
		output string
	}{
		{token + "\n", 0, "READY " + token + "\n"},
		{"wrong\n", 126, ""}, {"", 126, ""}, {token, 126, ""}, {strings.Repeat("x", 1024) + "\n", 126, ""},
	} {
		var output bytes.Buffer
		if code := helperMain(strings.NewReader(tc.input), &output, token); code != tc.code || output.String() != tc.output {
			t.Fatalf("handshake %q: code %d output %q", tc.input, code, output.String())
		}
	}
}

func TestHelperEnvironment(t *testing.T) {
	env := helperEnvironment(strings.Repeat("a", 64))
	if len(env) != 4 {
		t.Fatalf("unexpected inherited environment: %v", env)
	}
	for _, item := range env {
		if !strings.HasPrefix(item, "PATH=") && !strings.HasPrefix(item, "LC_ALL=") && !strings.HasPrefix(item, helperModeEnv+"=") && !strings.HasPrefix(item, helperTokenEnv+"=") {
			t.Fatalf("unexpected environment key")
		}
	}
}
