package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/llm"
)

type profileRoundTripper func(*http.Request) (*http.Response, error)

func (f profileRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func callProfileClient(client llm.Client, request llm.ChatRequest, streaming bool) (string, error) {
	if !streaming {
		response, err := client.Complete(context.Background(), request)
		return response.Content, err
	}
	events, err := client.Stream(context.Background(), request)
	if err != nil {
		return "", err
	}
	var content strings.Builder
	for event := range events {
		content.WriteString(event.Text)
		err = errors.Join(err, event.Err)
	}
	return content.String(), err
}

func profileSuccess(streaming bool) *http.Response {
	header := make(http.Header)
	body := `{"choices":[{"message":{"content":"{}"}}]}`
	if streaming {
		header.Set("Content-Type", "text/event-stream")
		body = "data: {\"choices\":[{\"delta\":{\"content\":\"{}\"}}]}\n\ndata: [DONE]\n\n"
	}
	return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

func TestRunProfileOverridesSurviveFallbackAndKeepConfiguredDefaults(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%t", streaming), func(t *testing.T) {
			var configuredCalls, profileCalls, transportCalls, callbacks atomic.Int32
			reply := func(w http.ResponseWriter) {
				response := profileSuccess(streaming)
				defer response.Body.Close()
				w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
				_, _ = io.Copy(w, response.Body)
			}
			configured := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				configuredCalls.Add(1)
				var body requestBody
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if r.URL.Path != "/current/v1/chat/completions" || body.Model != "current-model" || body.MaxTokens != 128 || body.Temperature != 0.7 || body.TopP != 0.9 || len(body.Stop) != 0 || body.ResponseFormat != nil || body.Stream != streaming {
					t.Error("configured defaults changed after a profiled request")
				}
				reply(w)
			}))
			defer configured.Close()
			profile := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := profileCalls.Add(1)
				var body requestBody
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if r.URL.Path != "/saved/v1/chat/completions" || body.Model != "saved-model" || body.MaxTokens != 512 || body.Temperature != 0 || body.TopP != 0.6 || body.Stream != streaming || len(body.Stop) != 1 || body.Stop[0] != "<|end|>" {
					t.Error("saved profile was not applied to the HTTP attempt")
				}
				if r.Header.Get("Authorization") != "Bearer current-key-fixture" {
					t.Error("profiled request did not use the configured API key")
				}
				if body.ResponseFormat == nil {
					t.Error("structured response format omitted")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if n == 1 {
					if body.ResponseFormat.Type != "json_schema" || callbacks.Load() != 0 {
						t.Error("initial attempt lost its schema or consumed a fallback charge")
					}
					w.WriteHeader(http.StatusUnprocessableEntity)
					_, _ = fmt.Fprint(w, `{"error":{"code":"unsupported_json_schema"}}`)
					return
				}
				if body.ResponseFormat.Type != "json_object" || body.ResponseFormat.JSONSchema != nil || callbacks.Load() != 1 {
					t.Error("fallback format or attempt charge is incorrect")
				}
				reply(w)
			}))
			defer profile.Close()

			client := New(configured.URL+"/current/v1", "current-key-fixture", "current-model", time.Second, 128, 0.7, 0.9, false)
			transport := client.httpClient.Transport
			client.httpClient.Transport = profileRoundTripper(func(request *http.Request) (*http.Response, error) {
				transportCalls.Add(1)
				return transport.RoundTrip(request)
			})
			guarded := llm.NewGuardedClient(client)
			request := llm.ChatRequest{
				Model: "saved-model", BaseURL: profile.URL + "/saved/v1/", Stop: []string{"<|end|>"},
				MaxTokens: 512, Temperature: 0, TemperatureSet: true, TopP: 0.6, TopPSet: true,
				JSONSchema: json.RawMessage(`{"type":"object"}`),
				BeforeFallbackAttempt: func() error {
					if profileCalls.Load() != 1 || configuredCalls.Load() != 0 {
						t.Error("fallback callback ran before the profile rejection")
					}
					callbacks.Add(1)
					return nil
				},
			}
			if content, err := callProfileClient(guarded, request, streaming); err != nil || content != "{}" {
				t.Fatalf("profiled content=%q error=%v", content, err)
			}
			if profileCalls.Load() != 2 || configuredCalls.Load() != 0 || callbacks.Load() != 1 || transportCalls.Load() != 2 {
				t.Fatalf("profile requests=%d configured requests=%d callbacks=%d transport calls=%d", profileCalls.Load(), configuredCalls.Load(), callbacks.Load(), transportCalls.Load())
			}
			if content, err := callProfileClient(guarded, llm.ChatRequest{}, streaming); err != nil || content != "{}" {
				t.Fatalf("default content=%q error=%v", content, err)
			}
			if configuredCalls.Load() != 1 || profileCalls.Load() != 2 || transportCalls.Load() != 3 || callbacks.Load() != 1 {
				t.Fatal("profile override leaked into a later request or replaced the configured HTTP client")
			}
		})
	}
}

func TestRequestProfileEndpointValidationBeforeHTTP(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, test := range []struct {
			name, endpoint string
			valid          bool
		}{
			{"loopback IPv4", "http://127.0.0.1:8000/v1", true},
			{"loopback IPv4 range", "http://127.2.3.4/v1", true},
			{"localhost HTTPS", "https://localhost:8443/v1", true},
			{"localhost case", "http://LOCALHOST/v1", true},
			{"loopback IPv6", "http://[::1]:8000/v1", true},
			{"mapped loopback IPv4", "http://[::ffff:127.0.0.1]/v1", true},
			{"relative", "/v1", false},
			{"opaque", "http:localhost/v1", false},
			{"no host", "http:///v1", false},
			{"non HTTP", "ftp://localhost/v1", false},
			{"remote host", "https://api.example.invalid/v1", false},
			{"private IP", "http://192.168.1.2/v1", false},
			{"unspecified IP", "http://0.0.0.0/v1", false},
			{"remote IPv6", "https://[2001:db8::1]/v1", false},
			{"localhost suffix", "http://localhost.example.invalid/v1", false},
			{"user info", "http://user:private-fixture@localhost/v1", false},
			{"query", "http://localhost/v1?key=private-fixture", false},
			{"empty query", "http://localhost/v1?", false},
			{"fragment", "http://localhost/v1#private-fixture", false},
			{"empty fragment", "http://localhost/v1#", false},
			{"invalid port", "http://localhost:private-fixture/v1", false},
			{"zero port", "http://localhost:0/v1", false},
			{"large port", "http://localhost:65536/v1", false},
			{"empty port", "http://localhost:/v1", false},
			{"invalid escape", "http://localhost/%private-fixture", false},
			{"IPv6 zone", "http://[::1%25lo]/v1", false},
		} {
			t.Run(fmt.Sprintf("streaming=%t/%s", streaming, test.name), func(t *testing.T) {
				var attempts, callbacks atomic.Int32
				client := New("http://127.0.0.1:8000/v1", "", "current-model", time.Second, 128, 0.7, 1, false)
				client.httpClient.Transport = profileRoundTripper(func(request *http.Request) (*http.Response, error) {
					attempts.Add(1)
					return profileSuccess(streaming), nil
				})
				_, err := callProfileClient(client, llm.ChatRequest{
					BaseURL: test.endpoint, Model: "saved-model", JSONSchema: json.RawMessage(`{"type":"object"}`),
					BeforeFallbackAttempt: func() error { callbacks.Add(1); return nil },
				}, streaming)
				wantAttempts := int32(0)
				if test.valid {
					wantAttempts = 1
				}
				if (err == nil) != test.valid || attempts.Load() != wantAttempts || callbacks.Load() != 0 {
					t.Fatalf("requests=%d callbacks=%d valid=%t error=%v", attempts.Load(), callbacks.Load(), test.valid, err)
				}
				if err != nil && (strings.Contains(err.Error(), test.endpoint) || strings.Contains(err.Error(), "private-fixture")) {
					t.Fatal("endpoint validation exposed the URL or credentials")
				}
			})
		}
	}
}

func TestRequestProfileModelValidationBeforeHTTP(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, test := range []struct {
			name, configured, override, want string
			valid                            bool
		}{
			{"default", "current-model", "", "current-model", true},
			{"override", "current-model", "local/model:Q4_K_M", "local/model:Q4_K_M", true},
			{"bounded", "current-model", strings.Repeat("m", 256), strings.Repeat("m", 256), true},
			{"empty effective model", "", "", "", false},
			{"blank", "current-model", " \t", "", false},
			{"long", "current-model", strings.Repeat("m", 257), "", false},
			{"surrounding whitespace", "current-model", " model ", "", false},
			{"control character", "current-model", "private-fixture\nmodel", "", false},
			{"invalid UTF8", "current-model", "model\xff", "", false},
		} {
			t.Run(fmt.Sprintf("streaming=%t/%s", streaming, test.name), func(t *testing.T) {
				var attempts atomic.Int32
				client := New("http://127.0.0.1:8000/v1", "", test.configured, time.Second, 128, 0.7, 1, false)
				client.httpClient.Transport = profileRoundTripper(func(request *http.Request) (*http.Response, error) {
					attempts.Add(1)
					var body requestBody
					if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body.Model != test.want {
						t.Error("effective model was not sent")
					}
					return profileSuccess(streaming), nil
				})
				_, err := callProfileClient(client, llm.ChatRequest{Model: test.override}, streaming)
				wantAttempts := int32(0)
				if test.valid {
					wantAttempts = 1
				}
				if (err == nil) != test.valid || attempts.Load() != wantAttempts {
					t.Fatalf("requests=%d valid=%t error=%v", attempts.Load(), test.valid, err)
				}
				if err != nil && strings.Contains(err.Error(), "private-fixture") {
					t.Fatal("model validation exposed the rejected value")
				}
			})
		}
	}
}

func TestChatRequestProfileMetadataDoesNotSerialize(t *testing.T) {
	request := llm.ChatRequest{
		Model: "saved-model", BaseURL: "http://127.0.0.1:8000/v1", Stop: []string{"<|end|>"},
		BeforeFallbackAttempt: func() error { return nil },
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"Model", "BaseURL", "Stop", "BeforeFallbackAttempt"} {
		if _, ok := fields[field]; ok {
			t.Errorf("adapter metadata %s was serialized", field)
		}
	}
}

func TestProfileTransportErrorRedactsURLAndPreservesCause(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%t", streaming), func(t *testing.T) {
			cause := fmt.Errorf("private-fixture: %w", context.DeadlineExceeded)
			client := New("http://127.0.0.1:8000/v1", "", "current-model", time.Second, 128, 0.7, 1, false)
			client.httpClient.Transport = profileRoundTripper(func(request *http.Request) (*http.Response, error) {
				return nil, cause
			})
			_, err := callProfileClient(client, llm.ChatRequest{BaseURL: "http://127.0.0.1:8001/private-fixture"}, streaming)
			if !errors.Is(err, cause) || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, llm.ErrModelTimeout) {
				t.Fatalf("transport error lost its cause or timeout classification: %v", err)
			}
			if strings.Contains(err.Error(), "private-fixture") || strings.Contains(err.Error(), "http://") {
				t.Fatal("transport error exposed the endpoint or underlying private details")
			}
		})
	}
}

func TestProfileEndpointDoesNotFollowRedirects(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%t", streaming), func(t *testing.T) {
			var destinationCalls, profileCalls atomic.Int32
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				destinationCalls.Add(1)
			}))
			defer destination.Close()
			profile := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				profileCalls.Add(1)
				w.Header().Set("Location", destination.URL+"/chat/completions")
				w.WriteHeader(http.StatusTemporaryRedirect)
			}))
			defer profile.Close()
			client := New(destination.URL, "current-key-fixture", "current-model", time.Second, 128, 0.7, 1, false)
			_, err := callProfileClient(client, llm.ChatRequest{BaseURL: profile.URL, Model: "saved-model"}, streaming)
			var httpError *HTTPError
			if !errors.As(err, &httpError) || httpError.StatusCode != http.StatusTemporaryRedirect || profileCalls.Load() != 1 || destinationCalls.Load() != 0 {
				t.Fatalf("profile requests=%d redirected requests=%d error=%v", profileCalls.Load(), destinationCalls.Load(), err)
			}
		})
	}
}
