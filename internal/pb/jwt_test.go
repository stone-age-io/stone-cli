package pb

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// An expired token is not a loud failure against PocketBase -- it serves the
// request as a guest, every list rule filters the result down to nothing, and
// the answer is 200 with an empty items array. A CLI that forwards it prints an
// empty table and says nothing, which is how a context can sit dead for weeks
// while every command appears to work. The client refuses one before it is sent,
// so these three cases are the difference between an error and a wrong answer.

// testToken builds an unsigned JWT with the given claims. The signature is
// nonsense on purpose: nothing here verifies one, and nothing should.
func testToken(claims map[string]any) string {
	payload, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".not-a-signature"
}

func TestTokenExpired(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  bool
	}{
		{
			name:  "expired an hour ago",
			token: testToken(map[string]any{"id": "abc123", "exp": time.Now().Add(-time.Hour).Unix()}),
			want:  true,
		},
		{
			name:  "valid for another hour",
			token: testToken(map[string]any{"id": "abc123", "exp": time.Now().Add(time.Hour).Unix()}),
			want:  false,
		},
		{
			// "Unknown" is not "expired". Refusing to send a token this CLI
			// could not parse would turn a claim rename into an outage, and the
			// server is the one that decides anyway.
			name:  "no exp claim",
			token: testToken(map[string]any{"id": "abc123"}),
			want:  false,
		},
		{
			name:  "not a jwt at all",
			token: "garbage",
			want:  false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TokenExpired(c.token); got != c.want {
				t.Errorf("TokenExpired = %v, want %v", got, c.want)
			}
		})
	}
}

func TestDecodeJWTExpiry(t *testing.T) {
	want := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	got, err := DecodeJWTExpiry(testToken(map[string]any{"exp": want.Unix()}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("got %s, want %s", got, want)
	}

	// A token with no exp is reported as the zero time and no error, so callers
	// can tell "unknown" from "expired".
	zero, err := DecodeJWTExpiry(testToken(map[string]any{"id": "abc123"}))
	if err != nil {
		t.Fatalf("a token without exp should not error: %v", err)
	}
	if !zero.IsZero() {
		t.Errorf("expected the zero time for a token with no exp, got %s", zero)
	}
}

func TestDecodeJWTUserID(t *testing.T) {
	id, err := DecodeJWTUserID(testToken(map[string]any{"id": "bg3v6n8rj664t5f"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "bg3v6n8rj664t5f" {
		t.Errorf("got %q", id)
	}

	if _, err := DecodeJWTUserID(testToken(map[string]any{"exp": 1})); err == nil {
		t.Error("a token with no id claim should error: org switch depends on it")
	}
}

// The 401 hint is appended by PBError itself so every command gets it without
// each one checking a status code.
func TestPBErrorHintsAtLoginOn401(t *testing.T) {
	unauthorized := (&PBError{Status: 401, Message: "The request requires valid record authorization token to be set."}).Error()
	if !strings.Contains(unauthorized, "stone auth login") {
		t.Errorf("a 401 should say what to do about it, got: %s", unauthorized)
	}

	// A 403 means the token is fine and the role is not; logging in again would
	// not change it, so the hint would be actively misleading.
	forbidden := (&PBError{Status: 403, Message: "You are not allowed to perform this request."}).Error()
	if strings.Contains(forbidden, "stone auth login") {
		t.Errorf("a 403 should not suggest logging in, got: %s", forbidden)
	}
}

// A 404 naming an unknown collection is what a CLI newer than its server looks
// like — `stone activity` against a pre-v0.8.0 platform, for instance. On its
// own PocketBase's wording reads like a client bug.
func TestPBErrorHintsAtAnOlderServerOnUnknownCollection(t *testing.T) {
	missing := (&PBError{Status: 404, Message: "Missing collection context."}).Error()
	if !strings.Contains(missing, "older than this CLI") {
		t.Errorf("an unknown-collection 404 should name the likely cause, got: %s", missing)
	}

	// An ordinary missing record must not pick up the same hint: the collection
	// is there, the row is not, and blaming the server version would send
	// someone off to check a release they do not need to check.
	notFound := (&PBError{Status: 404, Message: "The requested resource wasn't found."}).Error()
	if strings.Contains(notFound, "older than this CLI") {
		t.Errorf("a missing record should not blame the server version, got: %s", notFound)
	}
}
