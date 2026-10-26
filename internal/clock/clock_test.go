package clock

import (
	"testing"
	"time"
)

func TestFakeClock(t *testing.T) {
	t0 := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	f := NewFake(t0)
	var c Clock = f
	if !c.Now().Equal(t0) {
		t.Fatalf("Now = %v, want %v", c.Now(), t0)
	}

	ch := c.After(5 * time.Minute)
	f.Advance(4*time.Minute + 59*time.Second)
	select {
	case <-ch:
		t.Fatal("After fired early")
	default:
	}
	f.Advance(time.Second)
	select {
	case got := <-ch:
		if want := t0.Add(5 * time.Minute); !got.Equal(want) {
			t.Fatalf("After carried %v, want %v", got, want)
		}
	default:
		t.Fatal("After not ready")
	}

	select {
	case <-c.After(0):
	default:
		t.Fatal("After(0) not ready at once")
	}

	f.Set(t0.Add(-time.Hour))
	if !f.Now().Equal(t0.Add(-time.Hour)) {
		t.Fatalf("Set backwards: Now = %v", f.Now())
	}
}

func TestRealClock(t *testing.T) {
	var c Clock = Real{}
	if c.Now().IsZero() {
		t.Fatal("Real.Now is zero")
	}
	select {
	case <-c.After(time.Millisecond):
	case <-time.After(5 * time.Second):
		t.Fatal("Real.After never fired")
	}
}
