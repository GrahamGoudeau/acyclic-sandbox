# Working in acyclic-sandbox

- One Go binary: the mock vendor APIs (`internal/mock`), synthetic data (`internal/synth`), the capped model proxy (`internal/proxy`), run storage (`internal/store`, Postgres or memory) and the playground UI (`web/`).
- Toolchain: Go 1.23+. Dependencies: the Go standard library and pgx only.
- Vendor behavior lives in `internal/mock/vendors.go` and `fhir.go`. Every rule needs a public source in acyclic-sdk-spec's `vendors.md`.
- The playground runs the real JS SDK: `scripts/sync-sdk.sh ../acyclic-sdk-js` vendors it into `web/sdk/`. Do not edit `web/sdk/` by hand.
- Data is synthetic only. Never add a feature that sends requests to a real EHR from the hosted playground.
- Tests: `go test ./...`. End-to-end: run the server, then run the SDKs' e2e tests with `SANDBOX_URL`.
