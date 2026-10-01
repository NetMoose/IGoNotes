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

func TestRedactTailPrefixMatchesOriginalSemantics(t *testing.T) {
	// Exhaustively compare short overlapping/repetitive byte strings with the
	// previous suffix search. Keep the reference bounded and never print values.
	words := []string{""}
	for size := 1; size <= 7; size++ {
		for bits := 0; bits < 1<<size; bits++ {
			word := make([]byte, size)
			for i := range word {
				word[i] = 'a' + byte(bits>>i&1)
			}
			words = append(words, string(word))
		}
	}
	for _, secret := range words[1:] {
		for _, text := range words {
			want := strings.ReplaceAll(text, secret, "[REDACTED_REMOTE]")
			if !strings.HasSuffix(want, "[REDACTED_REMOTE]") {
				for n := min(len(secret)-1, len(want)); n >= 4; n-- {
					if strings.HasSuffix(want, secret[:n]) {
						want = want[:len(want)-n] + "[REDACTED_REMOTE]"
						break
					}
				}
			}
			if got := redact(text, []string{secret}); got != want {
				t.Fatal("tail-prefix matching changed redaction semantics")
			}
		}
	}
}

func TestRedactMultivariantReplacementMatchesOriginalSemantics(t *testing.T) {
	texts := []string{"", "credential credential", "abcXYZabcdef", "\x00abcd\xffabcd", "unicode-пример unicode", "aaaaab aaaab aaaaaac"}
	for size := 1; size <= 7; size++ {
		for bits := 0; bits < 1<<size; bits++ {
			text := make([]byte, size)
			for i := range text {
				text[i] = 'a' + byte(bits>>i&1)
			}
			texts = append(texts, string(text))
		}
	}
	for _, secrets := range [][]string{
		{"a", "aa", "aaa", "b"},
		{"ab", "aba", "abab", "baba", "aaab"},
		{"aaaaab", "aaaab", "aab", "baaaa", "aaaa"},
		{"credential", "REDACTED_REMOTE", "[RED"},
		{"abc", "abcXYZ", "XYZabcdef", "abcdef"},
		{"\x00abcd", "abcd", "unicode-пример", "unicode"},
	} {
		ordered := secretVariants(secrets)
		pairs := make([]string, 0, 2*len(ordered))
		for _, secret := range ordered {
			pairs = append(pairs, secret, "[REDACTED_REMOTE]")
		}
		for _, text := range texts {
			want := strings.NewReplacer(pairs...).Replace(text)
			if !strings.HasSuffix(want, "[REDACTED_REMOTE]") {
				tail := 0
				for _, secret := range ordered {
					for n := min(len(secret)-1, len(want)); n >= 4 && n > tail; n-- {
						if strings.HasSuffix(want, secret[:n]) {
							tail = n
							break
						}
					}
				}
				if tail > 0 {
					want = want[:len(want)-tail] + "[REDACTED_REMOTE]"
				}
			}
			if got := redact(text, secrets); got != want {
				t.Fatal("multivariant matcher changed replacement semantics")
			}
		}
	}
	// Sequential ReplaceAll calls would process the marker inserted by the
	// credential replacement. Public output must retain exactly one marker.
	if got := redact("credential", []string{"credential", "REDACTED_REMOTE"}); got != "[REDACTED_REMOTE]" {
		t.Fatal("replacement marker was treated as original diagnostic text")
	}
}

func TestRedactMultivariantAllocationsAreBounded(t *testing.T) {
	// Many adjacent matches must not cause a separate allocation per byte or
	// match. Count allocations only after constructing the diagnostic/secrets.
	for _, repeats := range []int{1024, 64 * 1024} {
		text := strings.Repeat("abc|", repeats)
		secrets := []string{"abc", "bc"}
		allocations := testing.AllocsPerRun(3, func() {
			got := redact(text, secrets)
			if strings.Count(got, "[REDACTED_REMOTE]") != repeats {
				t.Fatal("adjacent matches changed replacement behavior")
			}
		})
		if allocations > 32 {
			t.Fatalf("allocation bound exceeded for %d matches: %.0f allocations", repeats, allocations)
		}
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
