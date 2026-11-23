// Package store persists sandbox state: app registrations, per-app writes
// (the tenant overlay), and recorded agent runs. Postgres when DATABASE_URL
// is set; memory otherwise (local development).
//
// Everything stored here is synthetic. The hosted sandbox never talks to a
// real EHR, so no patient data can reach this database.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"
)

type App struct {
	ClientID   string            `json:"client_id"`
	Vendor     string            `json:"vendor"`
	Name       string            `json:"name"`
	JWKS       []json.RawMessage `json:"jwks,omitempty"`
	SecretHash string            `json:"-"`
	Created    time.Time         `json:"created"`
}

type Write struct {
	ClientID     string          `json:"client_id"`
	ResourceType string          `json:"resource_type"`
	ResourceID   string          `json:"resource_id"`
	Resource     json.RawMessage `json:"resource"`
	Created      time.Time       `json:"created"`
}

type Run struct {
	ID        string    `json:"id"`
	Session   string    `json:"-"` // browser session that owns the run
	Vendor    string    `json:"vendor"`
	PatientID string    `json:"patient_id"`
	Title     string    `json:"title"`
	Shared    bool      `json:"shared"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
}

type Event struct {
	Seq   int             `json:"seq"`
	Event json.RawMessage `json:"event"`
}

var ErrNotFound = errors.New("not found")

type Store interface {
	SaveApp(ctx context.Context, a App) error
	Apps(ctx context.Context) ([]App, error)
	AddWrite(ctx context.Context, w Write) error
	Writes(ctx context.Context, clientID string) ([]Write, error)
	SaveRun(ctx context.Context, r Run) error
	Run(ctx context.Context, id string) (Run, error)
	Runs(ctx context.Context, session string, limit int) ([]Run, error)
	AppendEvents(ctx context.Context, runID string, from int, events []json.RawMessage) error
	Events(ctx context.Context, runID string) ([]Event, error)
	// Purge deletes apps and writes older than appTTL and runs older than runTTL.
	Purge(ctx context.Context, appTTL, runTTL time.Duration) error
	Close()
}

type Memory struct {
	mu     sync.Mutex
	apps   map[string]App
	writes map[string][]Write
	runs   map[string]Run
	events map[string][]Event
}

func NewMemory() *Memory {
	return &Memory{apps: map[string]App{}, writes: map[string][]Write{}, runs: map[string]Run{}, events: map[string][]Event{}}
}

func (m *Memory) SaveApp(_ context.Context, a App) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.apps[a.ClientID] = a
	return nil
}

func (m *Memory) Apps(context.Context) ([]App, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []App
	for _, a := range m.apps {
		out = append(out, a)
	}
	return out, nil
}

func (m *Memory) AddWrite(_ context.Context, w Write) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes[w.ClientID] = append(m.writes[w.ClientID], w)
	return nil
}

func (m *Memory) Writes(_ context.Context, clientID string) ([]Write, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Write(nil), m.writes[clientID]...), nil
}

func (m *Memory) SaveRun(_ context.Context, r Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.runs[r.ID]; ok {
		r.Created = old.Created
	}
	m.runs[r.ID] = r
	return nil
}

func (m *Memory) Run(_ context.Context, id string) (Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[id]
	if !ok {
		return Run{}, ErrNotFound
	}
	return r, nil
}

func (m *Memory) Runs(_ context.Context, session string, limit int) ([]Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Run
	for _, r := range m.runs {
		if r.Session == session {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *Memory) AppendEvents(_ context.Context, runID string, from int, events []json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	have := m.events[runID]
	for i, e := range events {
		seq := from + i
		if seq < len(have) {
			continue // idempotent re-send
		}
		have = append(have, Event{Seq: seq, Event: e})
	}
	m.events[runID] = have
	return nil
}

func (m *Memory) Events(_ context.Context, runID string) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Event(nil), m.events[runID]...), nil
}

func (m *Memory) Purge(_ context.Context, appTTL, runTTL time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, a := range m.apps {
		if time.Since(a.Created) > appTTL {
			delete(m.apps, id)
			delete(m.writes, id)
		}
	}
	for id, r := range m.runs {
		if time.Since(r.Updated) > runTTL {
			delete(m.runs, id)
			delete(m.events, id)
		}
	}
	return nil
}

func (m *Memory) Close() {}
