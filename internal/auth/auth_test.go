package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/linsomniac/taskronin/internal/clock"
	"github.com/linsomniac/taskronin/internal/domain"
)

func TestParseTokenRejectsNonCanonical(t *testing.T) {
	raw, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.TokenID{1, 42, math.MaxInt64} {
		good := FormatToken(id, raw)
		if gotID, got, err := ParseToken(good); err != nil || gotID != id || got != raw {
			t.Fatalf("round trip: id=%v, secret matches=%v, err=%v", gotID, got == raw, err)
		}
	}
	sec := strings.TrimPrefix(FormatToken(42, raw), "tkr_42_")
	for i, bad := range []string{
		"", "tkr_0_" + sec, "tkr_042_" + sec, "tkr_-1_" + sec, "tkr_+1_" + sec,
		"tkr_42_" + sec[:42], "tkr_42_" + sec + "=", "tkr_9223372036854775808_" + sec,
		"tkr_10000000000000000000_" + sec, "TKR_42_" + sec, "tkr_42_" + sec[:42] + "!",
		"tkr_42_" + nonCanonicalBase64(sec, true), "tkr_42_" + sec + "\n",
		" tkr_42_" + sec, "tkr_42_" + sec[:20] + "\n" + sec[20:],
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if _, _, err := ParseToken(bad); err == nil {
				t.Fatal("accepted noncanonical token")
			}
		})
	}
}

func TestHashSecret(t *testing.T) {
	raw, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := HashSecret(raw), sha256.Sum256(raw[:]); got != want {
		t.Fatal("secret hash differs from SHA-256")
	}
	other, err := NewSecret()
	if err != nil || other == raw {
		t.Fatalf("new secret: distinct=%v, err=%v", other != raw, err)
	}
}

func TestOwnerTokenName(t *testing.T) {
	now := time.Date(2026, 10, 28, 10, 0, 0, 123456789, time.FixedZone("MDT", -6*60*60))
	name, err := OwnerTokenName(now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, "owner.20261028T160000.123Z-") ||
		!regexp.MustCompile(`^owner\.\d{8}T\d{6}\.\d{3}Z-[0-9a-f]{4}$`).MatchString(name) {
		t.Fatalf("unexpected owner token name: %q", name)
	}
}

func TestHashVerifyPassword(t *testing.T) {
	pw := rand.Text()
	encoded, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Fatal("unexpected password hash parameters")
	}
	parts := strings.Split(encoded, "$")
	salt, saltErr := base64.RawStdEncoding.DecodeString(parts[4])
	key, keyErr := base64.RawStdEncoding.DecodeString(parts[5])
	if saltErr != nil || keyErr != nil || len(salt) != 16 || len(key) != 32 {
		t.Fatal("unexpected password hash salt/key encoding")
	}
	for _, tc := range []struct {
		name string
		pw   string
		want bool
	}{{"right", pw, true}, {"wrong", pw + rand.Text(), false}} {
		t.Run(tc.name, func(t *testing.T) {
			if ok, err := VerifyPassword(encoded, tc.pw); err != nil || ok != tc.want {
				t.Fatalf("VerifyPassword = %v, %v; want %v", ok, err, tc.want)
			}
		})
	}
	// A second independently salted hash must differ for the same password.
	other, err := HashPassword(pw)
	if err != nil || other == encoded {
		t.Fatalf("fresh salt: different=%v, err=%v", other != encoded, err)
	}
	fast := hashForTest(pw)
	if ok, err := VerifyPassword(fast, pw); err != nil || !ok {
		t.Fatalf("encoded parameters were not used: %v, %v", ok, err)
	}
	boundaryKey := argon2.IDKey([]byte(pw), salt, 10, 32, 4, 32)
	boundaryHash := "$argon2id$v=19$m=32,t=10,p=4$" + parts[4] + "$" + base64.RawStdEncoding.EncodeToString(boundaryKey)
	if ok, err := VerifyPassword(boundaryHash, pw); err != nil || !ok {
		t.Fatalf("valid maximum time/parallelism: %v, %v", ok, err)
	}
	for i, bad := range []string{
		"", "garbage", strings.Replace(fast, "argon2id", "argon2i", 1),
		strings.Replace(fast, "v=19", "v=16", 1), fast + "$extra",
		strings.Replace(fast, "m=8", "m=1048577", 1),
		strings.Replace(fast, "t=1", "t=11", 1),
		strings.Replace(fast, "m=8,t=1,p=1", "m=40,t=1,p=5", 1),
		strings.Replace(fast, "m=8", "m=0", 1), strings.Replace(fast, "m=8", "m=7", 1),
		strings.Replace(fast, "t=1", "t=0", 1), strings.Replace(fast, "p=1", "p=0", 1),
		strings.Replace(fast, "m=8", "m=-1", 1), strings.Replace(fast, "t=1", "t=+1", 1),
		strings.Replace(fast, "m=8,t=1,p=1", "m=2056,t=1,p=257", 1),
		strings.Replace(fast, "m=8", "m=4294967296", 1),
		strings.Replace(fast, "t=1", "t=4294967296", 1),
		strings.Replace(fast, "m=8,t=1,p=1", "m=8,t=1,p=1,x=1", 1),
		strings.Replace(fast, "m=8,t=1,p=1", "m=8,t=1,t=1", 1),
		"$argon2id$v=19$m=8,t=1,p=1$" + parts[4] + "=$" + parts[5],
		"$argon2id$v=19$m=8,t=1,p=1$" + parts[4][:21] + "$" + parts[5],
		"$argon2id$v=19$m=8,t=1,p=1$" + nonCanonicalBase64(parts[4], false) + "$" + parts[5],
		"$argon2id$v=19$m=8,t=1,p=1$" + parts[4] + "$" + nonCanonicalBase64(parts[5], false),
		"$argon2id$v=19$m=8,t=1,p=1$" + parts[4] + "$" + parts[5][:42],
		"$argon2id$v=19$m=8,t=1,p=1$" + parts[4] + "$" + parts[5] + "=",
		"$argon2id$v=19$m=8,t=1,p=1$" + parts[4] + "$" + strings.Repeat("!", 43),
	} {
		t.Run(fmt.Sprintf("malformed_%d", i), func(t *testing.T) {
			if ok, err := VerifyPassword(bad, pw); ok || err == nil {
				t.Fatalf("malformed hash: ok=%v, err=%v", ok, err)
			}
		})
	}
}

func TestValidateNewPassword(t *testing.T) {
	for _, tc := range []struct {
		name string
		pw   string
		ok   bool
	}{
		{"empty", "", false}, {"seven", strings.Repeat("a", 7), false},
		{"eight", strings.Repeat("a", 8), true}, {"max", strings.Repeat("a", 1024), true},
		{"over_max", strings.Repeat("a", 1025), false}, {"invalid_utf8", strings.Repeat("\xff", 8), false},
		{"utf8_bytes", strings.Repeat("é", 4), true}, {"utf8_max", strings.Repeat("é", 512), true},
		{"utf8_over_max", strings.Repeat("é", 513), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateNewPassword(tc.pw); (err == nil) != tc.ok {
				t.Fatalf("ValidateNewPassword: err=%v; want valid=%v", err, tc.ok)
			}
			if !tc.ok {
				if encoded, err := HashPassword(tc.pw); err == nil || encoded != "" {
					t.Fatal("HashPassword accepted an invalid new password")
				}
			}
		})
	}
}

type observedClock struct {
	*clock.Fake
	waits chan time.Duration
}

func newObservedClock() *observedClock {
	return &observedClock{
		Fake:  clock.NewFake(time.Date(2026, 10, 28, 16, 0, 0, 0, time.UTC)),
		waits: make(chan time.Duration, 32),
	}
}

func (c *observedClock) After(d time.Duration) <-chan time.Time {
	ch := c.Fake.After(d)
	c.waits <- d // Timer is registered before the test can advance time.
	return ch
}

type verifyResult struct {
	ok  bool
	err error
}

func verifyAsync(ctx context.Context, g *PasswordGate, key, encoded, pw string) <-chan verifyResult {
	done := make(chan verifyResult, 1)
	go func() {
		ok, err := g.Verify(ctx, key, encoded, pw)
		done <- verifyResult{ok, err}
	}()
	return done
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for gate")
		var zero T
		return zero
	}
}

func requireVerified(t *testing.T, result verifyResult) {
	t.Helper()
	if !result.ok || result.err != nil {
		t.Fatalf("Verify = %v, %v", result.ok, result.err)
	}
}

func fillBurst(t *testing.T, g *PasswordGate, encoded, pw string) {
	t.Helper()
	for range 5 {
		requireVerified(t, receive(t, verifyAsync(t.Context(), g, "socket", encoded, pw)))
	}
}

func TestPasswordGate(t *testing.T) {
	pw := rand.Text()
	encoded := hashForTest(pw)
	t.Run("burst_and_per_key_delay", func(t *testing.T) {
		c := newObservedClock()
		g := NewPasswordGate(c)
		fillBurst(t, g, encoded, pw)
		select {
		case d := <-c.waits:
			t.Fatalf("initial burst waited %v", d)
		default:
		}
		done := verifyAsync(t.Context(), g, "socket", encoded, pw)
		if d := receive(t, c.waits); d != 5*time.Second {
			t.Fatalf("sixth verification delay = %v", d)
		}
		c.Advance(4 * time.Second)
		select {
		case result := <-done:
			t.Fatalf("verification completed early: %v", result)
		default:
		}
		c.Advance(time.Second)
		requireVerified(t, receive(t, done))
	})
	t.Run("max_wait_and_rejected_reservations", func(t *testing.T) {
		c := newObservedClock()
		g := NewPasswordGate(c)
		fillBurst(t, g, encoded, pw)
		var pending []<-chan verifyResult
		for _, want := range []time.Duration{5 * time.Second, 10 * time.Second, 15 * time.Second} {
			pending = append(pending, verifyAsync(t.Context(), g, "socket", encoded, pw))
			if d := receive(t, c.waits); d != want {
				t.Fatalf("delay = %v, want %v", d, want)
			}
		}
		for range 2 {
			result := receive(t, verifyAsync(t.Context(), g, "socket", encoded, pw))
			var de *domain.Error
			if result.ok || !errors.As(result.err, &de) || de.Code != domain.CodeRateLimited || de.RetryAfter != 20*time.Second {
				t.Fatalf("over-limit result = %+v", result)
			}
		}
		// Both rejected global reservations must also have been returned.
		other := verifyAsync(t.Context(), g, "other", encoded, pw)
		if d := receive(t, c.waits); d != 4*time.Second {
			t.Fatalf("global delay after rejection = %v, want 4s", d)
		}
		c.Advance(15 * time.Second)
		for _, done := range pending {
			requireVerified(t, receive(t, done))
		}
		requireVerified(t, receive(t, other))
	})
	t.Run("cancellation_restores_reservations", func(t *testing.T) {
		c := newObservedClock()
		g := NewPasswordGate(c)
		fillBurst(t, g, encoded, pw)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := verifyAsync(ctx, g, "socket", encoded, pw)
		if d := receive(t, c.waits); d != 5*time.Second {
			t.Fatalf("delay = %v", d)
		}
		cancel()
		if result := receive(t, done); result.ok || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("canceled wait = %+v", result)
		}
		done = verifyAsync(t.Context(), g, "socket", encoded, pw)
		if d := receive(t, c.waits); d != 5*time.Second {
			t.Fatalf("canceled reservation not returned: delay = %v", d)
		}
		c.Advance(5 * time.Second)
		requireVerified(t, receive(t, done))
	})
	t.Run("global_bucket_shared", func(t *testing.T) {
		c := newObservedClock()
		g := NewPasswordGate(c)
		for i := range 5 {
			requireVerified(t, receive(t, verifyAsync(t.Context(), g, fmt.Sprint(i), encoded, pw)))
		}
		done := verifyAsync(t.Context(), g, "fresh", encoded, pw)
		if d := receive(t, c.waits); d != time.Second {
			t.Fatalf("global delay = %v, want 1s", d)
		}
		c.Advance(time.Second)
		requireVerified(t, receive(t, done))
	})
	t.Run("already_canceled", func(t *testing.T) {
		c := newObservedClock()
		g := NewPasswordGate(c)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if ok, err := g.Verify(ctx, "socket", encoded, pw); ok || !errors.Is(err, context.Canceled) {
			t.Fatalf("already-canceled Verify = %v, %v", ok, err)
		}
		fillBurst(t, g, encoded, pw)
	})
	t.Run("failed_passwords_do_not_lock_out", func(t *testing.T) {
		c := newObservedClock()
		g := NewPasswordGate(c)
		for range 5 {
			result := receive(t, verifyAsync(t.Context(), g, "socket", encoded, pw+rand.Text()))
			if result.ok || result.err != nil {
				t.Fatalf("wrong password = %+v", result)
			}
		}
		done := verifyAsync(t.Context(), g, "socket", encoded, pw)
		if d := receive(t, c.waits); d != 5*time.Second {
			t.Fatalf("delay after failures = %v", d)
		}
		c.Advance(5 * time.Second)
		requireVerified(t, receive(t, done))
	})
	t.Run("semaphore_wait_is_cancelable", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			c := newObservedClock()
			g := NewPasswordGate(c)
			if cap(g.sem) != 1 {
				t.Fatal("gate must have one verification slot")
			}
			g.sem <- struct{}{} // Occupy the verification slot.
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := verifyAsync(ctx, g, "socket", encoded, pw)
			synctest.Wait()
			select {
			case result := <-done:
				t.Fatalf("verification bypassed occupied slot: %+v", result)
			default:
			}
			cancel()
			if result := receive(t, done); result.ok || !errors.Is(result.err, context.Canceled) {
				t.Fatalf("canceled semaphore wait = %+v", result)
			}
			<-g.sem
			fillBurst(t, g, encoded, pw)
		})
	})
	t.Run("malformed_hash_releases_semaphore", func(t *testing.T) {
		g := NewPasswordGate(newObservedClock())
		result := receive(t, verifyAsync(t.Context(), g, "socket", "", pw))
		if result.ok || result.err == nil {
			t.Fatalf("malformed hash = %+v", result)
		}
		requireVerified(t, receive(t, verifyAsync(t.Context(), g, "socket", encoded, pw)))
	})
	t.Run("lru_evicts_least_recently_used", func(t *testing.T) {
		c := newObservedClock()
		g := NewPasswordGate(c)
		for i := range 1024 {
			requireVerified(t, receive(t, verifyAsync(t.Context(), g, fmt.Sprint(i), encoded, pw)))
			c.Advance(time.Second)
		}
		original := g.perKey["0"]
		requireVerified(t, receive(t, verifyAsync(t.Context(), g, "0", encoded, pw)))
		c.Advance(time.Second)
		requireVerified(t, receive(t, verifyAsync(t.Context(), g, "new", encoded, pw)))
		if len(g.perKey) != 1024 || g.lru.Len() != 1024 || g.perKey["1"] != nil ||
			g.perKey["0"] != original || g.perKey["new"] == nil {
			t.Fatal("LRU did not preserve the touched bucket and evict the oldest bucket at capacity")
		}
	})
}

func TestSessionCookie(t *testing.T) {
	raw, cookie, err := NewSessionID()
	if err != nil || len(cookie) != 43 {
		t.Fatalf("NewSessionID: length=%d, err=%v", len(cookie), err)
	}
	if got, err := ParseSessionCookie(cookie); err != nil || got != raw {
		t.Fatalf("session round trip: matches=%v, err=%v", got == raw, err)
	}
	if got, want := HashSessionID(raw), sha256.Sum256(raw[:]); got != want {
		t.Fatal("session hash differs from SHA-256")
	}
	other, otherCookie, err := NewSessionID()
	if err != nil || other == raw || otherCookie == cookie {
		t.Fatalf("new session: distinct=%v, err=%v", other != raw && otherCookie != cookie, err)
	}
	for i, bad := range []string{"", cookie[:42], cookie + "=", cookie + "\n", " " + cookie,
		cookie[:20] + "\n" + cookie[20:], nonCanonicalBase64(cookie, true),
		cookie[:42] + "!", cookie[:42] + "+", cookie[:42] + "/"} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if _, err := ParseSessionCookie(bad); err == nil {
				t.Fatal("accepted noncanonical session cookie")
			}
		})
	}
}

func TestCSRFToken(t *testing.T) {
	secret, err := NewCSRFSecret()
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, secret[:])
	if _, err := mac.Write([]byte("tkr-csrf-v1")); err != nil {
		t.Fatal(err)
	}
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if got := CSRFToken(secret); got != want || len(got) != 43 {
		t.Fatal("CSRF token differs from the specified HMAC")
	}
	other, err := NewCSRFSecret()
	if err != nil || other == secret || CSRFToken(other) == want {
		t.Fatalf("new CSRF secret: distinct=%v, err=%v", other != secret, err)
	}
}

func nonCanonicalBase64(s string, url bool) string {
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	if url {
		alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	}
	last := strings.IndexByte(alphabet, s[len(s)-1])
	return s[:len(s)-1] + string(alphabet[last|1])
}

func FuzzParseToken(f *testing.F) {
	raw, err := NewSecret()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(FormatToken(42, raw))
	f.Add(FormatToken(math.MaxInt64, raw))
	f.Add("")
	f.Add("tkr_0_")
	f.Fuzz(func(t *testing.T, input string) {
		id, raw, err := ParseToken(input)
		if err == nil && FormatToken(id, raw) != input {
			t.Fatal("accepted token does not re-encode to its input")
		}
	})
}
