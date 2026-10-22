package notifications

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

type Repository interface {
	ClaimNotification(context.Context, string) (domain.NotificationDelivery, bool, error)
	FinishNotification(context.Context, domain.NotificationDelivery, bool) error
}
type Sender interface {
	Send(context.Context, domain.NotificationDelivery) error
}

func Dispatch(ctx context.Context, repo Repository, sender Sender, channel string) error {
	delivery, ok, err := repo.ClaimNotification(ctx, channel)
	if err != nil || !ok {
		return err
	}
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = sender.Send(sendCtx, delivery)
	cancel()
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer finishCancel()
	return repo.FinishNotification(finishCtx, delivery, err == nil)
}

func Run(ctx context.Context, repo Repository, sender Sender, channel string) {
	if channel == "none" {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = Dispatch(ctx, repo, sender, channel)
		}
	}
}

type provider struct {
	channel, endpoint, token string
	client                   *http.Client
}

func NewSender(channel, endpoint, token string) (Sender, error) {
	if channel != "desktop" && channel != "none" && channel != "ntfy" {
		return nil, errors.New("NOTIFICATION_CHANNEL must be desktop, none or ntfy")
	}
	if channel == "ntfy" {
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(endpoint) > 2048 {
			return nil, errors.New("NTFY_URL must be an HTTPS topic URL without credentials, query or fragment")
		}
	}
	return &provider{channel: channel, endpoint: endpoint, token: token, client: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func notificationBody(outcome string) string {
	switch outcome {
	case "completed":
		return "Исследование завершено."
	case "partial":
		return "Готов частичный результат исследования."
	case "blocked":
		return "Исследование остановлено: требуется проверка."
	case "budget_exhausted":
		return "Исследование остановлено по лимиту бюджета."
	case "cancelled":
		return "Исследование отменено."
	default:
		return "Исследование завершилось с ошибкой. Сохранённое состояние доступно в Qwenchik."
	}
}

func (p *provider) Send(ctx context.Context, delivery domain.NotificationDelivery) error {
	body := notificationBody(delivery.Outcome)
	if p.channel == "desktop" {
		binary, err := exec.LookPath("notify-send")
		if err != nil {
			return errors.New("desktop notification provider unavailable")
		}
		return exec.CommandContext(ctx, binary, "--app-name=Qwenchik", "--expire-time=8000", "--", "Qwenchik", body).Run()
	}
	if p.channel != "ntfy" {
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, strings.NewReader(body))
	if err != nil {
		return errors.New("notification request unavailable")
	}
	request.Header.Set("Title", "Qwenchik")
	request.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if p.token != "" {
		request.Header.Set("Authorization", "Bearer "+p.token)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return errors.New("notification endpoint unavailable")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("notification delivery rejected (%d)", response.StatusCode)
	}
	return nil
}
