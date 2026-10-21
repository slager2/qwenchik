package keepawake

import (
	"bufio"
	"encoding/hex"
	"io"
	"os"
)

const helperArg = "--qagent-keepawake-helper"
const helperModeEnv = "QAGENT_KEEPAWAKE_HELPER"
const helperTokenEnv = "QAGENT_KEEPAWAKE_TOKEN"

func init() {
	if len(os.Args) != 2 || os.Args[1] != helperArg {
		return
	}
	token := os.Getenv(helperTokenEnv)
	decoded, err := hex.DecodeString(token)
	if os.Getenv(helperModeEnv) != "v1" || err != nil || len(decoded) != 32 || len(token) != 64 {
		os.Exit(126)
	}
	os.Exit(helperMain(os.Stdin, os.Stdout, token))
}

func helperMain(input io.Reader, output io.Writer, token string) int {
	r := bufio.NewReaderSize(input, 128)
	line, err := r.ReadSlice('\n')
	if err != nil || string(line) != token+"\n" {
		return 126
	}
	if _, err := io.WriteString(output, "READY "+token+"\n"); err != nil {
		return 126
	}
	_, err = io.Copy(io.Discard, r)
	if err != nil {
		return 126
	}
	return 0
}

func helperEnvironment(token string) []string {
	return []string{"PATH=/usr/bin:/bin", "LC_ALL=C", helperModeEnv + "=v1", helperTokenEnv + "=" + token}
}
