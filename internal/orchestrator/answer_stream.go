package orchestrator

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

type answerStreamDecoder struct {
	mode    int
	pending string
	started bool
	closed  bool
	bytes   int
}

func finalizerOutputSchema() json.RawMessage {
	return encodeSchema(map[string]any{"type": "object", "properties": map[string]any{"answer": boundedStringSchema(1, maxSynthesisBytes)}, "required": []string{"answer"}, "additionalProperties": false})
}

func (d *answerStreamDecoder) Write(chunk string) (string, error) {
	d.bytes += len(chunk)
	if d.bytes > maxSynthesisBytes*6+128 {
		return "", errInvalidModelOutput
	}
	d.pending += chunk
	if d.mode == 0 {
		value := strings.TrimLeft(d.pending, " \r\n\t")
		if value == "" {
			return "", nil
		}
		if value[0] != '{' {
			return "", errInvalidModelOutput
		}
		d.mode = 2
	}
	if !d.started {
		colon := strings.IndexByte(d.pending, ':')
		if colon < 0 {
			if len(d.pending) > 64 {
				return "", errInvalidModelOutput
			}
			return "", nil
		}
		prefix := strings.TrimSpace(d.pending[:colon])
		var key string
		if len(prefix) < 2 || prefix[0] != '{' || json.Unmarshal([]byte(strings.TrimSpace(prefix[1:])), &key) != nil || key != "answer" {
			return "", errInvalidModelOutput
		}
		value := strings.TrimLeft(d.pending[colon+1:], " \r\n\t")
		if value == "" {
			return "", nil
		}
		if value[0] != '"' {
			return "", errInvalidModelOutput
		}
		d.pending = value[1:]
		d.started = true
	}
	var text strings.Builder
	position := 0
	for !d.closed && position < len(d.pending) {
		character := d.pending[position]
		if character == '"' {
			d.closed = true
			position++
			break
		}
		if character == '\\' {
			if len(d.pending)-position < 2 {
				break
			}
			escaped := d.pending[position+1]
			if escaped == 'u' {
				if len(d.pending)-position < 6 {
					break
				}
				code, err := strconv.ParseUint(d.pending[position+2:position+6], 16, 16)
				if err != nil {
					return "", errInvalidModelOutput
				}
				r := rune(code)
				size := 6
				if r >= 0xd800 && r <= 0xdbff {
					if len(d.pending)-position < 12 {
						break
					}
					if d.pending[position+6:position+8] != "\\u" {
						return "", errInvalidModelOutput
					}
					low, err := strconv.ParseUint(d.pending[position+8:position+12], 16, 16)
					if err != nil || low < 0xdc00 || low > 0xdfff {
						return "", errInvalidModelOutput
					}
					r = utf16.DecodeRune(r, rune(low))
					size = 12
				} else if utf16.IsSurrogate(r) {
					return "", errInvalidModelOutput
				}
				text.WriteRune(r)
				position += size
				continue
			}
			switch escaped {
			case '"', '\\', '/':
				text.WriteByte(escaped)
			case 'n':
				text.WriteByte('\n')
			case 'r':
				text.WriteByte('\r')
			case 't':
				text.WriteByte('\t')
			case 'b':
				text.WriteByte('\b')
			case 'f':
				text.WriteByte('\f')
			default:
				return "", errInvalidModelOutput
			}
			position += 2
			continue
		}
		if character < 0x20 {
			return "", errInvalidModelOutput
		}
		if !utf8.FullRuneInString(d.pending[position:]) {
			break
		}
		r, size := utf8.DecodeRuneInString(d.pending[position:])
		if r == utf8.RuneError && size == 1 {
			return "", errInvalidModelOutput
		}
		text.WriteRune(r)
		position += size
	}
	d.pending = d.pending[position:]
	if d.closed {
		trailing := strings.TrimSpace(d.pending)
		if trailing != "" && trailing != "}" {
			return "", errInvalidModelOutput
		}
	}
	return text.String(), nil
}

func (d *answerStreamDecoder) Finish() error {
	if d.mode != 2 || !d.closed || strings.TrimSpace(d.pending) != "}" {
		return errors.Join(errInvalidModelOutput, errors.New("finalizer stream ended before its answer object was complete"))
	}
	return nil
}
