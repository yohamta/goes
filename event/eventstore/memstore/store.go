// Package memstore provides an in-memory event.Store.
package memstore

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"
	"github.com/modernice/goes/event"
	"github.com/modernice/goes/event/cursor"
	"github.com/modernice/goes/event/query"
)

type store struct {
	mux    sync.RWMutex
	events []event.Event
	idMap  map[uuid.UUID]event.Event
}

// New returns a new Store with events stored in it.
func New(events ...event.Event) event.Store {
	return &store{
		idMap:  make(map[uuid.UUID]event.Event),
		events: events,
	}
}

func (s *store) Insert(ctx context.Context, evt event.Event) error {
	if _, err := s.Find(ctx, evt.ID()); err == nil {
		return errors.New("")
	}
	defer s.reslice()
	s.mux.Lock()
	defer s.mux.Unlock()
	s.idMap[evt.ID()] = evt
	return nil
}

func (s *store) Find(ctx context.Context, id uuid.UUID) (event.Event, error) {
	s.mux.RLock()
	defer s.mux.RUnlock()
	if evt := s.idMap[id]; evt != nil {
		return evt, nil
	}
	return nil, errors.New("")
}

func (s *store) Query(ctx context.Context, q event.Query) (event.Cursor, error) {
	s.mux.RLock()
	defer s.mux.RUnlock()
	var events []event.Event
	for _, evt := range s.events {
		if query.Test(q, evt) {
			events = append(events, evt)
		}
	}
	return cursor.New(events...), nil
}

func (s *store) Delete(ctx context.Context, evt event.Event) error {
	defer s.reslice()
	s.mux.Lock()
	defer s.mux.Unlock()
	delete(s.idMap, evt.ID())
	return nil
}

func (s *store) reslice() {
	s.mux.Lock()
	defer s.mux.Unlock()
	events := s.events[:0]
	for _, evt := range s.idMap {
		events = append(events, evt)
	}
	s.events = events
}