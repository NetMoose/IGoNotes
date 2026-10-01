package git

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestSecretVariantsAreDeterministic(t *testing.T) {
	secrets := []string{"https://beta:alph@example.invalid/repo", "zzzz", "aaaa", "zzzz", ""}
	want := []string{secrets[0], "beta:alph", "aaaa", "alph", "beta", "zzzz"}
	if got := secretVariants(secrets); !reflect.DeepEqual(got, want) {
		t.Fatal("secret variants are not deduplicated longest-first with lexical ties")
	}
	if secrets[0] != want[0] || secrets[1] != "zzzz" {
		t.Fatal("input secrets mutated")
	}
	if got := redact("aaaa-long aaaa", []string{"aaaa", "aaaa-long"}); got != "[REDACTED_REMOTE] [REDACTED_REMOTE]" {
		t.Fatal("shorter secret replaced before longer secret")
	}
}

func TestRedactExactEncodedHTTPUserinfo(t *testing.T) {
	const remote = "https://matrix%55ser:matrix%3apassword@example.invalid/repo"
	for _, variant := range []string{"matrix%55ser:matrix%3apassword", "matrixUser", "matrix:password"} {
		if got := redact("token="+variant, []string{remote}); got != "token=[REDACTED_REMOTE]" {
			t.Fatal("encoded HTTP credential variant was not redacted")
		}
	}
}

func TestRedactCompleteSecretBeforeOverlappingTailPrefix(t *testing.T) {
	if got := redact("token=abcdZZabcd", []string{"abcdZZabcd", "abcdLONG"}); got != "token=[REDACTED_REMOTE]" {
		t.Fatal("tail-prefix protection interrupted a complete secret replacement")
	}
}

func FuzzRedactGitDiagnostic(f *testing.F) {
	f.Add([]byte{0x12, 0xab}, "fatal: Authentication failed", uint16(64))
	f.Add([]byte("credential"), "Authorization: Basic", uint16(8))
	f.Add([]byte{0, 255}, "token=", uint16(65535))
	f.Add([]byte(strings.Repeat("s", 127)), "token=", uint16(64))
	f.Fuzz(func(t *testing.T, input []byte, diagnostic string, size uint16) {
		// Encode arbitrary fuzz bytes into credentials distinct from public messages
		// and redaction markers. Bound secrets to 4..256 and diagnostics to 64 KiB.
		if len(input) == 0 {
			input = []byte{0}
		}
		if len(input) > 127 {
			input = input[:127]
		}
		secret := "zQ" + hex.EncodeToString(input)
		password := secret[:min(len(secret), 210)]
		remote := "https://fuzzUser:" + password + "@example.invalid/repo.git"
		secrets := []string{secret, remote}
		variants := []string{remote, "fuzzUser:" + password, "fuzzUser", password, secret}
		limit := 1 + int(size)%4096
		if len(diagnostic) > 32*1024 {
			diagnostic = diagnostic[:32*1024]
		}
		value := diagnostic + "\n" + remote + "/ token=" + secret + " Authorization: Basic " + secret + " git@example.invalid:path/" + secret
		if len(value) > 64*1024 {
			t.Fatal("diagnostic bound exceeded")
		}
		for _, original := range []string{value, strings.Repeat("~", max(0, limit-6)) + secret + " tail"} {
			captureLimit := limit + longestString(secrets)
			captured, truncated := original, len(original) > captureLimit
			if truncated {
				captured = original[:captureLimit]
			}
			redacted, _ := redactAndLimit(captured, secrets, limit, truncated)
			if len(redacted) > limit {
				t.Fatal("redacted diagnostic exceeds limit")
			}
			assertNoSecretLeak(t, redacted, variants)
			safe := &SafeError{Code: CodeCommandFailed, Message: "Git command failed", diagnostic: redacted}
			encoded, err := json.Marshal(safe)
			if err != nil {
				t.Fatal("cannot encode safe error")
			}
			for _, public := range []string{safe.Error(), safe.Diagnostic(), fmt.Sprint(safe), fmt.Sprintf("%+v", safe), fmt.Sprintf("%#v", safe), fmt.Sprintf("%q", safe), string(encoded)} {
				assertNoSecretLeak(t, public, variants)
			}
		}
	})
}
