package tui

import (
	"testing"
	"time"

	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/client"
	"github.com/linsomniac/taskronin/internal/testutil"
)

func e2eModel(t *testing.T, c *client.Client, opts ...func(*Config)) *teatest.TestModel {
	t.Helper()
	clk, err := c.Clock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{API: c, Ctx: t.Context(), Clock: *clk, StartMode: ModeTriage, PollInterval: -1}
	for _, opt := range opts {
		opt(&cfg)
	}
	tm := teatest.NewTestModel(t, NewApp(cfg), teatest.WithInitialTermSize(100, 30))
	t.Cleanup(func() {
		if err := tm.Quit(); err != nil {
			t.Error(err)
		}
		tm.WaitFinished(t, teatest.WithFinalTimeout(15*time.Second))
	})
	return tm
}

func TestE2ETriageCardKeep(t *testing.T) { // TRI-10 end to end.
	srv := testutil.StartServer(t)
	c := srv.Client
	r, err := c.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: "Set up keycloak postgres cluster"})
	if err != nil {
		t.Fatal(err)
	}
	tm := e2eModel(t, c)
	teatest.WaitFor(t, tm.Output(), contains("Set up keycloak"), teatest.WithDuration(15*time.Second))
	tm.Send(key("k"))
	teatest.WaitFor(t, tm.Output(), contains("Triage clear"), teatest.WithDuration(15*time.Second))
	tm.Send(key("q"))
	tm.WaitFinished(t, teatest.WithFinalTimeout(15*time.Second))
	d, err := c.GetTask(t.Context(), r.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, len(d.Reasons), 0)
	eq(t, d.View, "backlog")
}

func TestE2EStaleCard(t *testing.T) { // TRI-19: a stale card must refresh without retrying.
	srv := testutil.StartServer(t)
	c := srv.Client
	r, err := c.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: "Set up keycloak postgres cluster"})
	if err != nil {
		t.Fatal(err)
	}
	tm := e2eModel(t, c)
	teatest.WaitFor(t, tm.Output(), contains("Set up keycloak"), teatest.WithDuration(15*time.Second))
	updated, err := c.UpdateTask(t.Context(), client.Mut{IfMatch: r.Task.ETag}, r.Task.ID,
		api.UpdateTaskRequest{Title: api.Opt[string]{Set: true, V: "Changed cluster task"}})
	if err != nil {
		t.Fatal(err)
	}
	tm.Send(key("k"))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return contains("changed elsewhere — refreshed")(b) && contains("Changed cluster task")(b)
	}, teatest.WithDuration(15*time.Second))
	tm.Send(key("q"))
	tm.WaitFinished(t, teatest.WithFinalTimeout(15*time.Second))
	d, err := c.GetTask(t.Context(), r.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, d.View, "triage")
	eq(t, len(d.Reasons), 1)
	eq(t, d.Reasons[0].Reason, "new")
	eq(t, d.ETag, updated.Task.ETag)
}

func TestE2ERecapWorked(t *testing.T) { // BLK-44 end to end, including yesterday's reconciliation.
	srv := testutil.StartServer(t, testutil.WithClock("2026-10-27 10:00"))
	c := srv.Client
	r, err := c.AddTask(t.Context(), client.Mut{}, api.AddTaskRequest{Title: "Set up keycloak postgres cluster"})
	if err != nil {
		t.Fatal(err)
	}
	date := testutil.D("2026-10-27")
	blocked, err := c.BlockTask(t.Context(), client.Mut{IfMatch: r.Task.ETag}, r.Task.ID,
		api.BlockRequest{Date: date, Start: 13 * 60, End: 15 * 60})
	if err != nil {
		t.Fatal(err)
	}
	srv.Clock.Set(testutil.Local(t, "2026-10-28 00:30"))
	tm := e2eModel(t, c, func(cfg *Config) { cfg.StartMode, cfg.RecapDate = ModeRecap, date })
	teatest.WaitFor(t, tm.Output(), contains("missed (auto)"), teatest.WithDuration(15*time.Second))
	tm.Send(key("w"))
	teatest.WaitFor(t, tm.Output(), contains("worked (user)"), teatest.WithDuration(15*time.Second))
	tm.Send(key("q"))
	tm.WaitFinished(t, teatest.WithFinalTimeout(15*time.Second))
	d, err := c.GetTask(t.Context(), r.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, len(d.Blocks), 1)
	eq(t, d.Blocks[0].ID, blocked.Block.ID)
	eq(t, d.Blocks[0].Status, "worked")
	eq(t, d.Blocks[0].OutcomeSource, ptr("user"))
	eq(t, len(d.Reasons), 1)
	eq(t, d.Reasons[0].Reason, "continue")
}
