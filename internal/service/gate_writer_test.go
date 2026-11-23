package service_test

import (
	"testing"
	"time"

	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/testutil"
)

// exhaustGate burns the per-key burst so the next verification waits on the fake clock.
func exhaustGate(t *testing.T, s *testutil.Scenario, key string) {
	t.Helper()
	for range 5 {
		if _, err := s.Svc.Login(s.Ctx(), service.LoginIn{RemoteKey: key, Password: "wrong-password"}); err == nil {
			t.Fatal("wrong password accepted")
		}
	}
}

func requireAddNotBlocked(t *testing.T, s *testutil.Scenario) {
	t.Helper()
	time.Sleep(300 * time.Millisecond) // let the gated call reach the gate wait
	done := make(chan struct{})
	go func() { s.Add("during gate wait"); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AddTask blocked behind a password-gate wait: writer is held")
	}
}

func TestLoginGateWaitDoesNotHoldWriter(t *testing.T) {
	s := testutil.NewScenario(t)
	exhaustGate(t, s, "gate-key")
	result := make(chan error, 1)
	go func() {
		_, err := s.Svc.Login(s.Ctx(), service.LoginIn{RemoteKey: "gate-key", Password: testutil.Password})
		result <- err
	}()
	requireAddNotBlocked(t, s)
	s.Clock.Advance(10 * time.Second)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestChangePasswordGateWaitDoesNotHoldWriter(t *testing.T) {
	s := testutil.NewScenario(t)
	exhaustGate(t, s, "socket")
	result := make(chan error, 1)
	go func() {
		result <- s.Svc.ChangePassword(s.Ctx(), s.Meta(),
			service.ChangePasswordIn{Current: testutil.Password, New: "another-long-password-1"})
	}()
	requireAddNotBlocked(t, s)
	s.Clock.Advance(10 * time.Second)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}
