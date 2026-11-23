package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Schema is plain Postgres (13+). It also runs on Neon, RDS and Aurora.
const Schema = `
CREATE TABLE IF NOT EXISTS sandbox_apps (
  client_id   text PRIMARY KEY,
  vendor      text NOT NULL,
  name        text NOT NULL DEFAULT '',
  jwks        jsonb,
  secret_hash text NOT NULL DEFAULT '',
  created     timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS sandbox_writes (
  id            bigserial PRIMARY KEY,
  client_id     text NOT NULL REFERENCES sandbox_apps(client_id) ON DELETE CASCADE,
  resource_type text NOT NULL,
  resource_id   text NOT NULL,
  resource      jsonb NOT NULL,
  created       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS sandbox_writes_client ON sandbox_writes (client_id);
CREATE TABLE IF NOT EXISTS sandbox_runs (
  id         text PRIMARY KEY,
  session    text NOT NULL,
  vendor     text NOT NULL,
  patient_id text NOT NULL DEFAULT '',
  title      text NOT NULL DEFAULT '',
  shared     boolean NOT NULL DEFAULT false,
  created    timestamptz NOT NULL DEFAULT now(),
  updated    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS sandbox_runs_session ON sandbox_runs (session, updated DESC);
CREATE TABLE IF NOT EXISTS sandbox_run_events (
  run_id  text NOT NULL REFERENCES sandbox_runs(id) ON DELETE CASCADE,
  seq     integer NOT NULL,
  event   jsonb NOT NULL,
  PRIMARY KEY (run_id, seq)
);
`

type Postgres struct{ db *pgxpool.Pool }

func OpenPostgres(ctx context.Context, url string) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 4 // small box, small pool
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(ctx, Schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Postgres{db}, nil
}

func (p *Postgres) Close() { p.db.Close() }

func (p *Postgres) SaveApp(ctx context.Context, a App) error {
	jwks, _ := json.Marshal(a.JWKS)
	_, err := p.db.Exec(ctx, `INSERT INTO sandbox_apps (client_id, vendor, name, jwks, secret_hash, created)
		VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (client_id) DO NOTHING`, a.ClientID, a.Vendor, a.Name, jwks, a.SecretHash, a.Created)
	return err
}

func (p *Postgres) Apps(ctx context.Context) ([]App, error) {
	rows, err := p.db.Query(ctx, `SELECT client_id, vendor, name, jwks, secret_hash, created FROM sandbox_apps`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []App
	for rows.Next() {
		var a App
		var jwks []byte
		if err := rows.Scan(&a.ClientID, &a.Vendor, &a.Name, &jwks, &a.SecretHash, &a.Created); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(jwks, &a.JWKS)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (p *Postgres) AddWrite(ctx context.Context, w Write) error {
	_, err := p.db.Exec(ctx, `INSERT INTO sandbox_writes (client_id, resource_type, resource_id, resource, created) VALUES ($1,$2,$3,$4,$5)`,
		w.ClientID, w.ResourceType, w.ResourceID, []byte(w.Resource), w.Created)
	return err
}

func (p *Postgres) Writes(ctx context.Context, clientID string) ([]Write, error) {
	rows, err := p.db.Query(ctx, `SELECT client_id, resource_type, resource_id, resource, created FROM sandbox_writes WHERE client_id = $1 ORDER BY id`, clientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Write
	for rows.Next() {
		var w Write
		var res []byte
		if err := rows.Scan(&w.ClientID, &w.ResourceType, &w.ResourceID, &res, &w.Created); err != nil {
			return nil, err
		}
		w.Resource = res
		out = append(out, w)
	}
	return out, rows.Err()
}

func (p *Postgres) SaveRun(ctx context.Context, r Run) error {
	_, err := p.db.Exec(ctx, `INSERT INTO sandbox_runs (id, session, vendor, patient_id, title, shared, created, updated)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (id) DO UPDATE SET patient_id = EXCLUDED.patient_id, title = EXCLUDED.title, shared = EXCLUDED.shared, updated = EXCLUDED.updated
		WHERE sandbox_runs.session = EXCLUDED.session`,
		r.ID, r.Session, r.Vendor, r.PatientID, r.Title, r.Shared, r.Created, r.Updated)
	return err
}

func (p *Postgres) Run(ctx context.Context, id string) (Run, error) {
	var r Run
	err := p.db.QueryRow(ctx, `SELECT id, session, vendor, patient_id, title, shared, created, updated FROM sandbox_runs WHERE id = $1`, id).
		Scan(&r.ID, &r.Session, &r.Vendor, &r.PatientID, &r.Title, &r.Shared, &r.Created, &r.Updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

func (p *Postgres) Runs(ctx context.Context, session string, limit int) ([]Run, error) {
	rows, err := p.db.Query(ctx, `SELECT id, session, vendor, patient_id, title, shared, created, updated FROM sandbox_runs WHERE session = $1 ORDER BY updated DESC LIMIT $2`, session, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.Session, &r.Vendor, &r.PatientID, &r.Title, &r.Shared, &r.Created, &r.Updated); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *Postgres) AppendEvents(ctx context.Context, runID string, from int, events []json.RawMessage) error {
	batch := &pgx.Batch{}
	for i, e := range events {
		batch.Queue(`INSERT INTO sandbox_run_events (run_id, seq, event) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, runID, from+i, []byte(e))
	}
	return p.db.SendBatch(ctx, batch).Close()
}

func (p *Postgres) Events(ctx context.Context, runID string) ([]Event, error) {
	rows, err := p.db.Query(ctx, `SELECT seq, event FROM sandbox_run_events WHERE run_id = $1 ORDER BY seq`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var b []byte
		if err := rows.Scan(&e.Seq, &b); err != nil {
			return nil, err
		}
		e.Event = b
		out = append(out, e)
	}
	return out, rows.Err()
}

func (p *Postgres) Purge(ctx context.Context, appTTL, runTTL time.Duration) error {
	if _, err := p.db.Exec(ctx, `DELETE FROM sandbox_apps WHERE created < $1`, time.Now().Add(-appTTL)); err != nil {
		return err
	}
	_, err := p.db.Exec(ctx, `DELETE FROM sandbox_runs WHERE updated < $1`, time.Now().Add(-runTTL))
	return err
}
