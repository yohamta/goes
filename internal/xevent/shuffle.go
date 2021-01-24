package xevent

import (
	"math/rand"
	"time"

	"github.com/modernice/goes/event"
)

// Shuffle shuffles events and returns the shuffled slice.
func Shuffle(events []event.Event) []event.Event {
	shuffled := make([]event.Event, len(events))
	copy(shuffled, events)
	rand.Seed(time.Now().UnixNano())
	rand.Shuffle(len(shuffled), func(i, j int) {
		shuffled[j], shuffled[i] = shuffled[i], shuffled[j]
	})
	return shuffled
}
