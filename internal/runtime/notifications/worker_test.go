package notifications

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
)

type dispatchFixture struct {
	claimed    bool
	finished   bool
	successful bool
}

func (r *dispatchFixture) ClaimNotification(context.Context, string) (domain.NotificationDelivery, bool, error) {
	r.claimed = true
	return domain.NotificationDelivery{TaskID: "task", ClaimID: "claim", Attempt: 1}, true, nil
}
func (r *dispatchFixture) FinishNotification(_ context.Context, _ domain.NotificationDelivery, successful bool) error {
	r.finished = true
	r.successful = successful
	return nil
}

type unavailableSender struct{ repo *dispatchFixture }

func (s unavailableSender) Send(context.Context, domain.NotificationDelivery) error {
	if !s.repo.claimed {
		panic("delivery started before durable claim")
	}
	return errors.New("private endpoint token")
}

func TestDispatchRecordsProviderFailureWithoutReturningTaskFailure(t *testing.T) {
	repo := &dispatchFixture{}
	if err := Dispatch(context.Background(), repo, unavailableSender{repo}, "desktop"); err != nil {
		t.Fatal(err)
	}
	if !repo.finished || repo.successful {
		t.Fatal("provider failure was not acknowledged independently")
	}
}

func TestNtfyUsesOnlyStaticOutcomeAndFailsWithoutRedirectOrSensitiveError(t *testing.T) {
	var body, authorization string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		authorization = r.Header.Get("Authorization")
		w.WriteHeader(503)
	}))
	defer server.Close()
	sender, err := NewSender("ntfy", server.URL+"/topic", "private-fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	provider := sender.(*provider)
	provider.client.Transport = server.Client().Transport
	err = sender.Send(context.Background(), domain.NotificationDelivery{TaskID: "PRIVATE_PROMPT", ClaimID: "PRIVATE_OUTPUT", Outcome: "budget_exhausted"})
	if err == nil || strings.Contains(err.Error(), "private-fixture") || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("unsafe error: %v", err)
	}
	if strings.Contains(body, "PRIVATE") || !strings.Contains(body, "лимиту бюджета") || authorization != "Bearer private-fixture-token" {
		t.Fatalf("unexpected bounded body: %q", body)
	}
	for _, endpoint := range []string{"http://example.org/topic", "https://user:password@example.org/topic", "https://example.org/topic?token=secret"} {
		if _, err := NewSender("ntfy", endpoint, ""); err == nil {
			t.Fatal("unsafe endpoint accepted")
		}
	}
}
