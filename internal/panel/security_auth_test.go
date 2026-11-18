package panel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/webauthn"
)

func TestSecurityPasskeyInvalidResponsesDoNotBlockOtherBrowsers(t *testing.T) {
	t.Setenv("NODO_MASTER_KEY", strings.Repeat("5a", 32))
	a, err := New(filepath.Join(t.TempDir(), "dummy.db"), strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, handle := []byte(strings.Repeat("i", 32)), []byte(strings.Repeat("h", 32))
	publicKey, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: key.X.FillBytes(make([]byte, 32)), -3: key.Y.FillBytes(make([]byte, 32))})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := json.Marshal(webauthn.Credential{ID: id, PublicKey: publicKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec("INSERT INTO auth_identity VALUES(1,?)", handle); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec("INSERT INTO auth_passkeys VALUES(?,?,?,?,?,?)", base64.RawURLEncoding.EncodeToString(id), "localhost", "dummy", string(credential), time.Now().Unix(), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	handler := a.Handler(fstest.MapFS{"index.html": {Data: []byte("fixture")}})
	request := func(route, peer string, cookie *http.Cookie, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://localhost:8080"+route, strings.NewReader(body))
		r.Header.Set("Origin", "http://localhost:8080")
		r.RemoteAddr = peer
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for i := 0; i < 5; i++ {
		begin := request(ceremonyPath+"login/begin", "192.0.2.10:12345", nil, `{}`)
		if begin.Code != http.StatusOK {
			t.Fatalf("begin %d: %d %s", i, begin.Code, begin.Body.String())
		}
		var cookie *http.Cookie
		for _, c := range begin.Result().Cookies() {
			if c.Name == ceremonyCookie {
				cookie = c
			}
		}
		if cookie == nil {
			t.Fatal("missing dummy ceremony")
		}
		finish := request(ceremonyPath+"login/finish", "192.0.2.10:12345", cookie, `{}`)
		if finish.Code != http.StatusUnauthorized {
			t.Fatalf("invalid response unexpectedly accepted: %d", finish.Code)
		}
		if replay := request(ceremonyPath+"login/finish", "192.0.2.10:12345", cookie, `{}`); replay.Code != http.StatusBadRequest {
			t.Fatalf("used invalid-response challenge was not consumed: %d", replay.Code)
		}
		t.Logf("invalid response %d status=%d", i+1, finish.Code)
	}
	other := request(ceremonyPath+"login/begin", "192.0.2.20:23456", nil, `{}`)
	if other.Code != http.StatusOK {
		t.Fatalf("another browser blocked after five unauthenticated failures: %d %s", other.Code, other.Body.String())
	}
	var options struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(other.Body.Bytes(), &options); err != nil || options.PublicKey.Challenge == "" {
		t.Fatal("missing legitimate browser challenge", err)
	}
	var cookie *http.Cookie
	for _, c := range other.Result().Cookies() {
		if c.Name == ceremonyCookie {
			cookie = c
		}
	}
	clientData, _ := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": options.PublicKey.Challenge, "origin": "http://localhost:8080"})
	rpHash, clientHash := sha256.Sum256([]byte("localhost")), sha256.Sum256(clientData)
	authData := append(append([]byte{}, rpHash[:]...), 0x05)
	authData = binary.BigEndian.AppendUint32(authData, 1)
	digest := sha256.Sum256(append(append([]byte{}, authData...), clientHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	assertion, _ := json.Marshal(map[string]any{"id": b64(id), "rawId": b64(id), "type": "public-key", "response": map[string]any{"authenticatorData": b64(authData), "clientDataJSON": b64(clientData), "signature": b64(signature), "userHandle": b64(handle)}})
	finish := request(ceremonyPath+"login/finish", "192.0.2.20:23456", cookie, string(assertion))
	if finish.Code != http.StatusOK {
		t.Fatalf("legitimate signed response rejected after unrelated failures: %d %s", finish.Code, finish.Body.String())
	}
	foundSession := false
	for _, c := range finish.Result().Cookies() {
		foundSession = foundSession || c.Name == sessionCookie && validToken(c.Value)
	}
	if !foundSession {
		t.Fatal("valid signature did not produce an authenticated session")
	}
}
