package mock

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/GrahamGoudeau/acyclic-sandbox/internal/store"
)

// App is a self-serve sandbox registration: one per visitor. Its client id
// is also the tenant key that isolates the visitor's writes.
type App = store.App

type token struct {
	clientID, vendor string
	scopes           []string
	expires          time.Time
}

type Auth struct {
	db     store.Store
	mu     sync.Mutex
	apps   map[string]*App
	tokens map[string]*token
	jtis   map[string]time.Time
}

// NewAuth loads registrations from db so they survive restarts.
func NewAuth(ctx context.Context, db store.Store) (*Auth, error) {
	a := &Auth{db: db, apps: map[string]*App{}, tokens: map[string]*token{}, jtis: map[string]time.Time{}}
	apps, err := db.Apps(ctx)
	if err != nil {
		return nil, err
	}
	for i := range apps {
		a.apps[apps[i].ClientID] = &apps[i]
	}
	return a, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func hashSecret(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// Register creates an app. With a JWKS it uses private_key_jwt; without, it
// gets a generated client secret (returned once).
func (a *Auth) Register(ctx context.Context, vendor, name string, jwks []json.RawMessage) (*App, string, error) {
	if len(jwks) > 4 {
		return nil, "", errors.New("at most 4 keys")
	}
	for _, k := range jwks {
		if _, _, err := parseJWK(k); err != nil {
			return nil, "", fmt.Errorf("bad jwk: %w", err)
		}
	}
	app := &App{ClientID: "sbx-" + randHex(10), Vendor: vendor, Name: name, JWKS: jwks, Created: time.Now()}
	secret := ""
	if len(jwks) == 0 {
		secret = "sbs_" + randHex(18)
		app.SecretHash = hashSecret(secret)
	}
	if err := a.db.SaveApp(ctx, *app); err != nil {
		return nil, "", err
	}
	a.mu.Lock()
	a.apps[app.ClientID] = app
	a.mu.Unlock()
	return app, secret, nil
}

func (a *Auth) App(id string) *App {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.apps[id]
}

// Expire drops apps and tokens older than ttl (the nightly reset).
func (a *Auth) Expire(ttl time.Duration) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var gone []string
	for id, app := range a.apps {
		if time.Since(app.Created) > ttl {
			delete(a.apps, id)
			gone = append(gone, id)
		}
	}
	for k, t := range a.tokens {
		if time.Now().After(t.expires) || a.apps[t.clientID] == nil {
			delete(a.tokens, k)
		}
	}
	for j, exp := range a.jtis {
		if time.Now().After(exp) {
			delete(a.jtis, j)
		}
	}
	return gone
}

// OAuthError is an RFC 6749 error response.
type OAuthError struct {
	Status int
	Code   string
	Desc   string
}

func (e *OAuthError) Error() string { return e.Code + ": " + e.Desc }

func oerr(code, desc string) *OAuthError { return &OAuthError{400, code, desc} }

// VerifyAssertion checks a SMART Backend Services client assertion against
// the app's registered keys: alg RS384/ES384, iss = sub = client_id,
// aud = this token URL, exp at most 5 minutes out, jti not reused.
func (a *Auth) VerifyAssertion(assertion, tokenURL, vendor string) (*App, error) {
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		return nil, oerr("invalid_client", "client_assertion is not a JWT")
	}
	var hdr struct{ Alg, Kid, Typ string }
	var cl struct {
		Iss, Sub, Jti string
		Aud           any
		Exp, Nbf, Iat float64
	}
	if err := decodeSeg(parts[0], &hdr); err != nil {
		return nil, oerr("invalid_client", "bad JWT header")
	}
	if err := decodeSeg(parts[1], &cl); err != nil {
		return nil, oerr("invalid_client", "bad JWT claims")
	}
	if hdr.Alg != "RS384" && hdr.Alg != "ES384" {
		return nil, oerr("invalid_client", "alg must be RS384 or ES384")
	}
	app := a.App(cl.Iss)
	if app == nil || app.Vendor != vendor {
		return nil, oerr("invalid_client", "unknown client_id for this vendor")
	}
	if cl.Sub != cl.Iss {
		return nil, oerr("invalid_client", "sub must equal iss")
	}
	if !audMatches(cl.Aud, tokenURL) {
		return nil, oerr("invalid_client", "aud must be the token endpoint URL")
	}
	now := float64(time.Now().Unix())
	if cl.Exp == 0 || cl.Exp < now-30 {
		return nil, oerr("invalid_client", "assertion expired")
	}
	if cl.Exp > now+300+30 {
		return nil, oerr("invalid_client", "exp may be at most 5 minutes in the future")
	}
	if cl.Nbf != 0 && cl.Nbf > now+30 {
		return nil, oerr("invalid_client", "assertion not yet valid")
	}
	if cl.Jti == "" {
		return nil, oerr("invalid_client", "jti is required")
	}
	signed := []byte(parts[0] + "." + parts[1])
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, oerr("invalid_client", "bad signature encoding")
	}
	ok := false
	for _, raw := range app.JWKS {
		pub, kid, err := parseJWK(raw)
		if err != nil || (hdr.Kid != "" && kid != "" && kid != hdr.Kid) {
			continue
		}
		if verify(hdr.Alg, pub, signed, sig) {
			ok = true
			break
		}
	}
	if !ok {
		return nil, oerr("invalid_client", "signature does not match a registered key")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := app.ClientID + "/" + cl.Jti
	if _, seen := a.jtis[key]; seen {
		return nil, oerr("invalid_client", "jti already used")
	}
	a.jtis[key] = time.Unix(int64(cl.Exp)+60, 0)
	return app, nil
}

func (a *Auth) VerifySecret(clientID, secret, vendor string) (*App, error) {
	app := a.App(clientID)
	if app == nil || app.Vendor != vendor || app.SecretHash == "" ||
		subtle.ConstantTimeCompare([]byte(app.SecretHash), []byte(hashSecret(secret))) != 1 {
		return nil, &OAuthError{401, "invalid_client", "client authentication failed"}
	}
	return app, nil
}

func (a *Auth) Issue(app *App, scopes []string, ttl time.Duration) string {
	tok := "sbx_at_" + randHex(24)
	a.mu.Lock()
	a.tokens[tok] = &token{clientID: app.ClientID, vendor: app.Vendor, scopes: scopes, expires: time.Now().Add(ttl)}
	a.mu.Unlock()
	return tok
}

// Check validates a bearer token for vendor and returns its client id and scopes.
func (a *Auth) Check(bearer, vendor string) (string, []string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.tokens[bearer]
	if t == nil || t.vendor != vendor || time.Now().After(t.expires) {
		return "", nil, false
	}
	return t.clientID, t.scopes, true
}

func audMatches(aud any, want string) bool {
	switch v := aud.(type) {
	case string:
		return v == want
	case []any:
		for _, x := range v {
			if s, _ := x.(string); s == want {
				return true
			}
		}
	}
	return false
}

func decodeSeg(s string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func parseJWK(raw json.RawMessage) (crypto.PublicKey, string, error) {
	var k struct{ Kty, N, E, Crv, X, Y, Kid string }
	if err := json.Unmarshal(raw, &k); err != nil {
		return nil, "", err
	}
	dec := func(s string) (*big.Int, error) {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil || len(b) == 0 {
			return nil, errors.New("bad base64url")
		}
		return new(big.Int).SetBytes(b), nil
	}
	switch k.Kty {
	case "RSA":
		n, err := dec(k.N)
		if err != nil {
			return nil, "", err
		}
		e, err := dec(k.E)
		if err != nil {
			return nil, "", err
		}
		if n.BitLen() < 2048 {
			return nil, "", errors.New("RSA keys must be at least 2048 bits")
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, k.Kid, nil
	case "EC":
		if k.Crv != "P-384" {
			return nil, "", errors.New("EC keys must use P-384")
		}
		x, err := dec(k.X)
		if err != nil {
			return nil, "", err
		}
		y, err := dec(k.Y)
		if err != nil {
			return nil, "", err
		}
		return &ecdsa.PublicKey{Curve: elliptic.P384(), X: x, Y: y}, k.Kid, nil
	}
	return nil, "", errors.New("kty must be RSA or EC")
}

func verify(alg string, pub crypto.PublicKey, signed, sig []byte) bool {
	h := sha512.Sum384(signed)
	switch alg {
	case "RS384":
		k, ok := pub.(*rsa.PublicKey)
		return ok && rsa.VerifyPKCS1v15(k, crypto.SHA384, h[:], sig) == nil
	case "ES384":
		k, ok := pub.(*ecdsa.PublicKey)
		if !ok || len(sig) != 96 {
			return false
		}
		r, s := new(big.Int).SetBytes(sig[:48]), new(big.Int).SetBytes(sig[48:])
		return ecdsa.Verify(k, h[:], r, s)
	}
	return false
}
