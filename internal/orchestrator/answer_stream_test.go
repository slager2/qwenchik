package orchestrator

import (
	"strings"
	"testing"
)

func TestAnswerStreamDecoderPreservesTextAcrossEscapesAndUTF8Boundaries(t *testing.T) {
	input := ` { "answer": "Библиотеки\nQuoted: \"source\" \\path \uD83D\uDE00" } `
	for stride := 1; stride <= len(input); stride++ {
		decoder := answerStreamDecoder{}
		var text strings.Builder
		for start := 0; start < len(input); start += stride {
			part, err := decoder.Write(input[start:min(len(input), start+stride)])
			if err != nil {
				t.Fatalf("stride=%d: %v", stride, err)
			}
			text.WriteString(part)
		}
		if err := decoder.Finish(); err != nil {
			t.Fatal(err)
		}
		if text.String() != "Библиотеки\nQuoted: \"source\" \\path 😀" {
			t.Fatalf("stride=%d decoded=%q", stride, text.String())
		}
	}
}

func TestAnswerStreamDecoderRejectsTruncatedOrWrongEnvelope(t *testing.T) {
	for _, input := range []string{`{"answer":"unfinished`, `{"other":"text"}`, `{"answer":"text","other":1}`, `{"answer":"\uD800"}`, "```json\n{\"answer\":\"fenced\"}\n```", `["array"]`, "plain answer"} {
		decoder := answerStreamDecoder{}
		_, err := decoder.Write(input)
		if err == nil {
			err = decoder.Finish()
		}
		if err == nil {
			t.Errorf("invalid finalizer envelope accepted: %q", input)
		}
	}
}
