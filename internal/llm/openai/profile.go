package openai

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/llm"
)

func (c *Client) resolveRequest(request llm.ChatRequest) (llm.ChatRequest, error) {
	if request.BaseURL == "" {
		request.BaseURL = c.baseURL
	}
	endpoint, err := url.Parse(request.BaseURL)
	if err != nil || endpoint.Host == "" || endpoint.Opaque != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return llm.ChatRequest{}, errors.New("local LLM base URL must be an absolute http(s) URL")
	}
	if endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || strings.Contains(request.BaseURL, "#") {
		return llm.ChatRequest{}, errors.New("local LLM base URL must not contain credentials, query parameters, or a fragment")
	}
	host := endpoint.Hostname()
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return llm.ChatRequest{}, errors.New("local LLM base URL must use a loopback host")
	}
	if port := endpoint.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return llm.ChatRequest{}, errors.New("local LLM base URL port must be between 1 and 65535")
		}
	} else if strings.HasSuffix(endpoint.Host, ":") {
		return llm.ChatRequest{}, errors.New("local LLM base URL port must not be empty")
	}
	if request.Model == "" {
		request.Model = c.model
	}
	if request.Model == "" || len(request.Model) > 256 || strings.TrimSpace(request.Model) != request.Model || !utf8.ValidString(request.Model) || strings.IndexFunc(request.Model, unicode.IsControl) >= 0 {
		return llm.ChatRequest{}, errors.New("local LLM model must be a nonempty name of at most 256 bytes without surrounding whitespace or control characters")
	}
	request.BaseURL = strings.TrimRight(request.BaseURL, "/")
	return request, nil
}
