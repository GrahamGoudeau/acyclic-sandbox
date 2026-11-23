# acyclic-sandbox

Mock EHR APIs that behave like Epic, Oracle Health, athenahealth, MEDITECH and eClinicalWorks, plus a browser playground where an AI agent works a synthetic chart through real FHIR calls. Every part is open source (Apache-2.0).

- **Playground:** [agentsdk.acyclic.sh](https://agentsdk.acyclic.sh). Pick a vendor and a patient, then ask a question. You see each tool call, each FHIR request and response, the telemetry your logs would hold, and the chart as the agent reads and writes it. Every tool call has "copy as JS, Go, Rust or curl", and every run can be shared as a read-only link.
- **Mock APIs:** [sandbox.acyclic.sh](https://sandbox.acyclic.sh). Point any FHIR client at them. They use real SMART auth (signed JWTs are verified against your registered key) and enforce each vendor's documented rules: required parameters, paging style, id formats, error shapes, rate limits and write rules.

All patients are generated: 12 synthetic charts with problems, medications, allergies, about 1,100 observations, encounters, notes, appointments and coverage. One patient has four years of monthly results, so clients that skip paging visibly lose data.

## Use the mock APIs

```sh
# Register an app. Signed-JWT vendors (epic, ecw) need your public key; the others return a secret.
curl -s https://sandbox.acyclic.sh/register -d '{"vendor":"oracle"}'
# {"client_id":"sbx-...","client_secret":"sbs_...","fhir_base_url":".../oracle/r4/ec2458f2-...","token_url":"...","auth":"client_secret_basic"}

TOKEN=$(curl -s -u "$CLIENT_ID:$CLIENT_SECRET" "$TOKEN_URL" \
  -d grant_type=client_credentials -d scope="system/Patient.read system/Observation.read" | jq -r .access_token)

curl -s "$FHIR_BASE/Patient?family=Diaz&given=Josefina" -H "Authorization: Bearer $TOKEN" -H "Accept: application/fhir+json"
```

| Vendor | FHIR base path | Auth |
|---|---|---|
| Epic | `/epic/interconnect-fhir-oauth/api/FHIR/R4` | signed JWT |
| Oracle Health | `/oracle/r4/ec2458f2-1e24-41c8-b71b-0e701af7583d` | client secret or signed JWT |
| athenahealth | `/athena/fhir/r4` (+ `/athena/v1/195900/...`) | client secret or signed JWT |
| MEDITECH | `/meditech/v1/uscore/R4` | client secret or signed JWT |
| eClinicalWorks | `/ecw/fhir/r4/SBXACY` | signed JWT |

Each base serves `/metadata` and `/.well-known/smart-configuration`. Registrations and anything you write are removed after 7 days. The sources for every vendor rule are in [acyclic-sdk-spec/vendors.md](https://github.com/GrahamGoudeau/acyclic-sdk-spec/blob/main/vendors.md). Fidelity varies by vendor: Epic and Oracle Health have the most public documentation, and MEDITECH the least.

## Run it yourself

```sh
go run ./cmd/sandbox                        # http://localhost:8190, memory store, chat off
DATABASE_URL=postgres://... \
OPENROUTER_API_KEY=... \
PUBLIC_MOCK_BASE=https://sandbox.example.com \
TRUST_PROXY=1 go run ./cmd/sandbox          # Postgres, chat on, behind a reverse proxy
```

| Env | Default | Purpose |
|---|---|---|
| `PORT` | 8190 | Listen port (127.0.0.1; `LISTEN_ALL=1` for all interfaces) |
| `DATABASE_URL` | memory | Postgres for apps, writes and recorded runs (plain SQL, works on Neon, RDS, Aurora) |
| `PUBLIC_MOCK_BASE` | `http://localhost:PORT` | Public origin used in links and token audiences |
| `OPENROUTER_API_KEY` | none | Model proxy key; without it the playground's chat is off |
| `MODEL` / `MODEL_UPSTREAM` | DeepSeek via OpenRouter | Any OpenAI-compatible API |
| `TRUST_PROXY` | off | Read client IPs from `X-Forwarded-For` for rate limits |

## How it is built

- **`internal/synth`:** a deterministic generator. Same ids and values on every start, with dates anchored to the day the server started.
- **`internal/mock`:** one search engine over canonical resources, plus a vendor layer that rewrites ids and references into each vendor's format and applies each vendor's rules. Each registered app is a tenant that sees the shared charts plus only its own writes.
- **`internal/proxy`:** the playground's model calls go through here.
  - The model is pinned; only the SDK's tools are accepted; output tokens are capped.
  - Limits apply per IP (per 10 minutes and per day) and globally per day.
  - OpenRouter requests use zero data retention.
- **`web/`:** vanilla JS. It runs [acyclic-sdk-js](https://github.com/GrahamGoudeau/acyclic-sdk-js) in the browser (vendored into `web/sdk` by `scripts/sync-sdk.sh`), so the playground is a live demo of the SDK itself.

## Synthetic data only

The hosted playground talks only to these mocks. The SDK's `dataPolicy: "synthetic"` refuses other hosts, and the model behind the playground has no BAA. Never paste real patient information. To use real EHR data, run the SDK in your own environment with a BAA-covered model.

## License

Apache-2.0. Copyright 2026 Acyclic LLC. Not affiliated with or endorsed by Epic Systems, Oracle, athenahealth, MEDITECH or eClinicalWorks. Vendor names describe API compatibility only.
