package eventbus

import (
	"context"
	"fmt"

	"github.com/modernice/goes/event"
	"github.com/modernice/goes/internal/concurrent"
)

// Await is a shortcut for NewAwaiter(bus).Once(ctx, names...). See Awaiter.Once
// for documentation.
func Await[D any](ctx context.Context, bus event.Bus, names ...string) (<-chan event.Of[D], <-chan error, error) {
	return NewAwaiter[D](bus).Once(ctx, names...)
}

// Awaiter can be used to await events in more complex scenarios.
type Awaiter[D any] struct {
	bus event.Bus
}

// NewAwaiter returns an Awaiter for the given Bus.
func NewAwaiter[D any](bus event.Bus) Awaiter[D] {
	return Awaiter[D]{bus}
}

// Once subscribes to the given events. After the first received event, the
// subscription is canceled, so that the returned event and error channels will
// never receive more than one event or one error, respectively.
//
// If len(names) == 0, Once returns nil channels.
func (a Awaiter[D]) Once(ctx context.Context, names ...string) (<-chan event.Of[D], <-chan error, error) {
	if len(names) == 0 {
		return nil, nil, nil
	}

	ctx, cancel := context.WithCancel(ctx)

	events, errs, err := a.bus.Subscribe(ctx, names...)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("subscribe to %q events: %w", names, err)
	}

	out := make(chan event.Of[D])
	outErrs, fail := concurrent.Errors(ctx)

	go func() {
		defer close(out)
		defer cancel()

		select {
		case <-ctx.Done():
			fail(ctx.Err())
			return
		case err := <-errs:
			fail(err)
			return
		case evt := <-events:
			casted, ok := event.TryCast[D](evt)
			if !ok {
				var to D
				fail(fmt.Errorf("failed to cast event [from=%T, to=%T]", evt, to))
				return
			}

			select {
			case <-ctx.Done():
				fail(ctx.Err())
				return
			case out <- casted:
			}
		}
	}()

	return out, outErrs, nil
}
