package llm

import (
	"context"
	"errors"
	"testing"
)

type probeClient struct {
	response ChatResponse
	err      error
	request  ChatRequest
}

func (p *probeClient) Complete(_ context.Context, request ChatRequest) (ChatResponse, error) {
	p.request = request
	return p.response, p.err
}

func (*probeClient) Stream(context.Context, ChatRequest) (<-chan ChatEvent, error) {
	return nil, errors.New("not used")
}

func TestProbeStructuredOutputUsesBoundedStrictSchema(t *testing.T) {
	client := &probeClient{response: ChatResponse{Content: `{"ok":true}`}}
	if err := ProbeStructuredOutput(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	if len(client.request.JSONSchema) == 0 || client.request.JSONMode || client.request.MaxTokens > 24 || len(client.request.Messages) != 2 {
		t.Fatalf("doctor probe request was not bounded JSON Schema mode: %#v", client.request)
	}
}

func TestProbeStructuredOutputRejectsInvalidResponse(t *testing.T) {
	for _, response := range []string{`{"ok":false}`, `{"ok":true,"extra":1}`, `not json`} {
		t.Run(response, func(t *testing.T) {
			client := &probeClient{response: ChatResponse{Content: response}}
			if err := ProbeStructuredOutput(context.Background(), client); err == nil {
				t.Fatalf("invalid smoke response %q was accepted", response)
			}
		})
	}
}
