package resources

import (
	"encoding/json"
	"errors"
	"strings"
)

var ErrModelResource = errors.New("model resource unavailable")
var ErrModelOOM = errors.New("model out of memory")

func ClassifyModelHTTPError(status int, body []byte) error {
	if status != 500 && status != 503 && status != 507 {
		return nil
	}
	if len(body) == 0 || len(body) > 4096 {
		return nil
	}
	var payload struct {
		Error struct {
			Code    json.RawMessage `json:"code"`
			Message string          `json:"message"`
			Type    string          `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	var rawCode string
	_ = json.Unmarshal(payload.Error.Code, &rawCode)
	code := strings.ToLower(rawCode)
	typ := strings.ToLower(payload.Error.Type)
	msg := strings.ToLower(strings.TrimSpace(payload.Error.Message))
	if code == "out_of_memory" || code == "oom" || typ == "out_of_memory" || strings.Contains(msg, "out of memory") {
		return errors.Join(ErrModelResource, ErrModelOOM)
	}
	return nil
}
