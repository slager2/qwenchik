package llm

import "context"

type GuardedClient struct {
	client Client
	slot   chan struct{}
}

func NewGuardedClient(client Client) *GuardedClient {
	return &GuardedClient{client: client, slot: make(chan struct{}, 1)}
}

func (g *GuardedClient) acquire(ctx context.Context, wait bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !wait {
		select {
		case g.slot <- struct{}{}:
			return nil
		default:
			return ErrInferenceBusy
		}
	}
	select {
	case g.slot <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-g.slot
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *GuardedClient) Complete(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	if err := g.acquire(ctx, true); err != nil {
		return ChatResponse{}, err
	}
	defer func() { <-g.slot }()
	return g.client.Complete(ctx, request)
}

func (g *GuardedClient) TryComplete(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	if err := g.acquire(ctx, false); err != nil {
		return ChatResponse{}, err
	}
	defer func() { <-g.slot }()
	return g.client.Complete(ctx, request)
}

func (g *GuardedClient) Stream(ctx context.Context, request ChatRequest) (<-chan ChatEvent, error) {
	if err := g.acquire(ctx, true); err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	started := false
	defer func() {
		if !started {
			cancel()
			<-g.slot
		}
	}()
	upstream, err := g.client.Stream(streamCtx, request)
	if err != nil {
		return nil, err
	}
	if upstream == nil {
		return nil, ErrInvalidModelOutput
	}
	events := make(chan ChatEvent, 16)
	started = true
	go func() {
		defer close(events)
		defer func() { <-g.slot }()
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				cancel()
				for range upstream {
				}
				return
			case event, ok := <-upstream:
				if !ok {
					return
				}
				select {
				case events <- event:
				case <-ctx.Done():
					cancel()
					for range upstream {
					}
					return
				}
			}
		}
	}()
	return events, nil
}
