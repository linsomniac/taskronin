package service_test

import (
	"database/sql"
	"io"
	"testing"

	"taskronin/internal/backup"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/testutil"
)

func TestSCP01(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("read-only target")
	expect := s.Expect(id)
	ro := s.As(testutil.TokenActor("ro", domain.ScopeRead))
	before := s.OpCount()
	_, err := ro.Svc.AddTask(ro.Ctx(), ro.Meta(), service.AddTaskIn{Title: "forbidden"})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	_, err = ro.Svc.AddEntry(ro.Ctx(), ro.Meta(), service.AddEntryIn{Title: "forbidden"})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	_, err = ro.Svc.Place(ro.Ctx(), ro.Meta(), service.PlaceIn{ID: id, Expect: expect, How: domain.PlaceKeep})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	if _, err := ro.Svc.Today(ro.Ctx(), ro.Actor); err != nil {
		t.Fatal(err)
	}
	if s.OpCount() != before || s.Task(id).Version != expect.Version {
		t.Fatal("read-only requests mutated the fixture")
	}
}

func TestSCP02(t *testing.T) {
	cases := scopeCases()
	for _, tc := range []struct {
		method    string
		forbidden bool
	}{
		{"Drop", true}, {"Delete", true}, {"DeleteEntry", true}, {"UpdateEntry", true}, {"Reopen/remove-entry", true},
		{"Complete", false}, {"Defer", false}, {"Block", false}, {"Place", false}, {"UpdateTask", false},
		{"Undelete", false}, {"AddEntry", false}, {"AddTask", false},
	} {
		t.Run(tc.method, func(t *testing.T) {
			for _, method := range cases {
				if method.name == tc.method {
					s := testutil.NewScenario(t)
					err := method.call(s, s.As(testutil.AI()).Meta())
					requireScopeOutcome(t, err, tc.forbidden)
					return
				}
			}
			t.Fatalf("missing scope case for %s", tc.method)
		})
	}
}

func TestSCP08(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("write-only target")
	expect := s.Expect(id)
	w := s.As(testutil.TokenActor("w", domain.ScopeWrite))
	r := s.Must(w.Svc.Place(w.Ctx(), w.Meta(), service.PlaceIn{ID: id, Expect: expect, How: domain.PlaceKeep}))
	if r.Task == nil || r.Task.ID != id || r.Task.Version != expect.Version+1 || len(s.Reasons(id)) != 0 {
		t.Fatalf("write-only placement did not acknowledge triage: %+v", r)
	}
	_, err := w.Svc.GetTask(w.Ctx(), w.Actor, id)
	testutil.RequireCode(t, err, domain.CodeForbidden)
}

type scopeCase struct {
	name              string
	required          domain.Scopes
	ownerOnly, notWeb bool
	// Explicit expected forbidden outcomes: missing bit, exact bits, web owner.
	forbidden [3]bool
	call      func(*testutil.Scenario, service.Meta) error
}

func TestScopeMatrix(t *testing.T) {
	for _, tc := range scopeCases() {
		t.Run(tc.name, func(t *testing.T) {
			// The flags document the independent actor restrictions in A§7.3;
			// expectations are supplied by each row, never computed from scopes.
			if tc.forbidden[1] != tc.ownerOnly || tc.forbidden[2] != tc.notWeb {
				t.Fatal("actor restrictions and explicit expectations disagree")
			}
			check := func(t *testing.T, a domain.Actor, forbidden bool) {
				t.Helper()
				s := testutil.NewScenario(t)
				requireScopeOutcome(t, tc.call(s, s.As(a).Meta()), forbidden)
			}
			for _, bit := range []domain.Scopes{domain.ScopeRead, domain.ScopeCapture, domain.ScopeWrite, domain.ScopeDestroy, domain.ScopeAdmin} {
				if tc.required.Has(bit) {
					t.Run("missing/"+bit.String(), func(t *testing.T) {
						check(t, testutil.TokenActor("matrix", domain.AllScopes&^bit), tc.forbidden[0])
					})
				}
			}
			t.Run("exact", func(t *testing.T) {
				check(t, testutil.TokenActor("matrix", tc.required), tc.forbidden[1])
			})
			t.Run("web", func(t *testing.T) {
				a := testutil.Owner()
				a.Channel = domain.ChanWeb
				check(t, a, tc.forbidden[2])
			})
			if tc.ownerOnly {
				t.Run("all-scopes-token", func(t *testing.T) {
					check(t, testutil.TokenActor("matrix", domain.AllScopes), true)
				})
			}
		})
	}
}

func requireScopeOutcome(t *testing.T, err error, forbidden bool) {
	t.Helper()
	if forbidden {
		testutil.RequireCode(t, err, domain.CodeForbidden)
	} else if err != nil {
		// Valid fixtures make the allowed path stronger than merely not forbidden.
		t.Fatalf("authorized operation failed: %v", err)
	}
}

// scopeCases covers the public scope-controlled operations in A§7.3, including
// the admin reads ListTokens/BackupStatus/Export. Authentication, maintenance
// and offline-only system methods do not take these three actor shapes.
func scopeCases() []scopeCase {
	return []scopeCase{
		{name: "AddTask", required: domain.ScopeCapture, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				_, err := s.Svc.AddTask(s.Ctx(), m, service.AddTaskIn{Title: "captured"})
				return err
			}},
		{name: "AddTask/placement", required: domain.ScopeCapture | domain.ScopeWrite, ownerOnly: true, forbidden: [3]bool{true, true, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				_, err := s.Svc.AddTask(s.Ctx(), m, service.AddTaskIn{Title: "placed", Place: &service.AddPlacement{How: "bottom"}})
				return err
			}},
		{name: "AddEntry", required: domain.ScopeCapture, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				_, err := s.Svc.AddEntry(s.Ctx(), m, service.AddEntryIn{Title: "captured entry"})
				return err
			}},
		{name: "UpdateTask", required: domain.ScopeWrite, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				ref := scopeTask(s)
				title := "updated"
				_, err := s.Svc.UpdateTask(s.Ctx(), m, service.UpdateTaskIn{ID: ref.ID, Expect: ref.Expect, Title: &title})
				return err
			}},
		{name: "SetDue", required: domain.ScopeWrite, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				ref, due := scopeTask(s), testutil.Moment("10-29")
				_, err := s.Svc.SetDue(s.Ctx(), m, service.SetDueIn{ID: ref.ID, Expect: ref.Expect, Due: &due})
				return err
			}},
		{name: "Place", required: domain.ScopeWrite, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				ref := scopeTask(s)
				_, err := s.Svc.Place(s.Ctx(), m, service.PlaceIn{ID: ref.ID, Expect: ref.Expect, How: domain.PlaceKeep})
				return err
			}},
		{name: "AcceptNew", required: domain.ScopeWrite, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				ref := scopeTask(s)
				_, err := s.Svc.AcceptNew(s.Ctx(), m, service.AcceptNewIn{Items: []service.AcceptItem{{ID: ref.ID, Expect: ref.Expect}}})
				return err
			}},
		{name: "Defer", required: domain.ScopeWrite, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				ref, until := scopeTask(s), testutil.Moment("10-29")
				_, err := s.Svc.Defer(s.Ctx(), m, service.DeferIn{ID: ref.ID, Expect: ref.Expect, Until: &until})
				return err
			}},
		{name: "Defer/none", required: domain.ScopeWrite, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				id := s.Add("deferred", testutil.DeferTo("10-29"))
				_, err := s.Svc.Defer(s.Ctx(), m, service.DeferIn{ID: id, Expect: s.Expect(id)})
				return err
			}},
		{name: "MarkManual", required: domain.ScopeWrite, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				ref := scopeTask(s)
				_, err := s.Svc.MarkManual(s.Ctx(), m, service.MarkManualIn(ref))
				return err
			}},
		{name: "Block", required: domain.ScopeWrite, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				ref := scopeTask(s)
				_, err := s.Svc.Block(s.Ctx(), m, service.BlockIn{TaskID: ref.ID, Expect: ref.Expect, Span: scopeSpan()})
				return err
			}},
		{name: "Reblock", required: domain.ScopeWrite, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				ref := scopeBlock(s)
				b := s.BlockView(ref.BlockID)
				_, err := s.Svc.Reblock(s.Ctx(), m, service.ReblockIn{BlockID: b.ID, Expect: ref.Expect, TaskExpect: s.Expect(b.TaskID), Span: scopeSpan()})
				return err
			}},
		{name: "Unblock", required: domain.ScopeWrite, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				_, err := s.Svc.Unblock(s.Ctx(), m, scopeBlock(s))
				return err
			}},
		{name: "SetOutcome", required: domain.ScopeWrite, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				ref := scopeBlock(s)
				_, err := s.Svc.SetOutcome(s.Ctx(), m, service.SetOutcomeIn{BlockID: ref.BlockID, Expect: ref.Expect, Outcome: domain.SetWorked})
				return err
			}},
		{name: "Complete", required: domain.ScopeWrite, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				ref := scopeTask(s)
				_, err := s.Svc.Complete(s.Ctx(), m, service.CompleteIn{ID: ref.ID, Expect: ref.Expect})
				return err
			}},
		{name: "Reopen", required: domain.ScopeWrite, forbidden: [3]bool{true, false, false}, call: scopeReopen(false)},
		{name: "Reopen/remove-entry", required: domain.ScopeWrite | domain.ScopeDestroy, forbidden: [3]bool{true, false, false}, call: scopeReopen(true)},
		{name: "Drop", required: domain.ScopeDestroy, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				_, err := s.Svc.Drop(s.Ctx(), m, scopeTask(s))
				return err
			}},
		{name: "Delete", required: domain.ScopeDestroy, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				_, err := s.Svc.Delete(s.Ctx(), m, scopeTask(s))
				return err
			}},
		{name: "Undelete", required: domain.ScopeWrite, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				id := s.Add("deleted task")
				s.Delete(id)
				_, err := s.Svc.Undelete(s.Ctx(), m, service.TaskRef{ID: id, Expect: s.Expect(id)})
				return err
			}},
		{name: "UpdateEntry", required: domain.ScopeDestroy, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				ref, title := scopeEntry(s), "updated entry"
				_, err := s.Svc.UpdateEntry(s.Ctx(), m, service.UpdateEntryIn{ID: ref.ID, Expect: ref.Expect, Title: &title})
				return err
			}},
		{name: "RelinkEntry", required: domain.ScopeWrite, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				ref, task := scopeEntry(s), scopeTask(s)
				_, err := s.Svc.RelinkEntry(s.Ctx(), m, service.RelinkEntryIn{ID: ref.ID, Expect: ref.Expect, TaskID: sql.Null[domain.TaskID]{V: task.ID, Valid: true}})
				return err
			}},
		{name: "DeleteEntry", required: domain.ScopeDestroy, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				_, err := s.Svc.DeleteEntry(s.Ctx(), m, scopeEntry(s))
				return err
			}},
		{name: "UndeleteEntry", required: domain.ScopeDestroy, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				ref := scopeEntry(s)
				s.DeleteEntry(ref.ID)
				_, err := s.Svc.UndeleteEntry(s.Ctx(), m, service.EntryRef{ID: ref.ID, Expect: s.EntryExpect(ref.ID)})
				return err
			}},
		{name: "SetFocus", required: domain.ScopeWrite, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				ref := scopeTask(s)
				_, err := s.Svc.SetFocus(s.Ctx(), m, service.SetFocusIn{TaskID: sql.Null[domain.TaskID]{V: ref.ID, Valid: true}})
				return err
			}},
		{name: "MarkAIReviewed", required: domain.ScopeWrite, ownerOnly: true, forbidden: [3]bool{true, true, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				ai := s.As(testutil.AI())
				ai.Add("review me")
				_, err := s.Svc.MarkAIReviewed(s.Ctx(), m, service.MarkAIReviewedIn{Through: s.Op(ai.Last.OpID).Seq})
				return err
			}},
		{name: "CreateToken", required: domain.ScopeAdmin, notWeb: true, forbidden: [3]bool{true, false, true},
			call: func(s *testutil.Scenario, m service.Meta) error {
				_, err := s.Svc.CreateToken(s.Ctx(), m, service.CreateTokenIn{Name: "created", Scopes: domain.ScopeRead})
				return err
			}},
		{name: "RevokeToken", required: domain.ScopeAdmin, notWeb: true, forbidden: [3]bool{true, false, true},
			call: func(s *testutil.Scenario, m service.Meta) error {
				token, err := s.Svc.CreateToken(s.Ctx(), s.Meta(), service.CreateTokenIn{Name: "revoke", Scopes: domain.ScopeRead})
				if err != nil {
					s.T.Fatal(err)
				}
				_, err = s.Svc.RevokeToken(s.Ctx(), m, service.RevokeTokenIn{ID: token.Token.ID})
				return err
			}},
		{name: "ListTokens", required: domain.ScopeAdmin, notWeb: true, forbidden: [3]bool{true, false, true},
			call: func(s *testutil.Scenario, m service.Meta) error {
				_, err := s.Svc.ListTokens(s.Ctx(), m.Actor)
				return err
			}},
		{name: "UpdateSettings", required: domain.ScopeAdmin, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				daily := 30
				_, err := s.Svc.UpdateSettings(s.Ctx(), m, service.UpdateSettingsIn{KeepDaily: &daily})
				return err
			}},
		{name: "ChangePassword", required: domain.ScopeAdmin, ownerOnly: true, forbidden: [3]bool{true, true, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				return s.Svc.ChangePassword(s.Ctx(), m, service.ChangePasswordIn{Current: testutil.Password, New: domain.NewOpID().String()})
			}},
		{name: "RunBackup", required: domain.ScopeAdmin, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				_, err := s.Svc.RunBackup(s.Ctx(), m.Actor, backup.KindManual)
				return err
			}},
		{name: "BackupStatus", required: domain.ScopeAdmin, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				_, err := s.Svc.BackupStatus(s.Ctx(), m.Actor)
				return err
			}},
		{name: "Export", required: domain.ScopeAdmin, forbidden: [3]bool{true, false, false},
			call: func(s *testutil.Scenario, m service.Meta) error {
				return s.Svc.Export(s.Ctx(), m.Actor, io.Discard)
			}},
		{name: "Revert/capture", required: domain.ScopeCapture, forbidden: [3]bool{true, false, false}, call: scopeReversal("Revert", domain.ScopeCapture)},
		{name: "Revert/write", required: domain.ScopeWrite, forbidden: [3]bool{true, false, false}, call: scopeReversal("Revert", domain.ScopeWrite)},
		{name: "Revert/destroy", required: domain.ScopeDestroy, forbidden: [3]bool{true, false, false}, call: scopeReversal("Revert", domain.ScopeDestroy)},
		{name: "Undo/capture", required: domain.ScopeCapture, forbidden: [3]bool{true, false, false}, call: scopeReversal("Undo", domain.ScopeCapture)},
		{name: "Undo/write", required: domain.ScopeWrite, forbidden: [3]bool{true, false, false}, call: scopeReversal("Undo", domain.ScopeWrite)},
		{name: "Undo/destroy", required: domain.ScopeDestroy, forbidden: [3]bool{true, false, false}, call: scopeReversal("Undo", domain.ScopeDestroy)},
		{name: "Redo/capture", required: domain.ScopeCapture, forbidden: [3]bool{true, false, false}, call: scopeReversal("Redo", domain.ScopeCapture)},
		{name: "Redo/write", required: domain.ScopeWrite, forbidden: [3]bool{true, false, false}, call: scopeReversal("Redo", domain.ScopeWrite)},
		{name: "Redo/destroy", required: domain.ScopeDestroy, forbidden: [3]bool{true, false, false}, call: scopeReversal("Redo", domain.ScopeDestroy)},
	}
}

func scopeTask(s *testutil.Scenario) service.TaskRef {
	s.T.Helper()
	id := s.Add("scope target")
	return service.TaskRef{ID: id, Expect: s.Expect(id)}
}

func scopeEntry(s *testutil.Scenario) service.EntryRef {
	s.T.Helper()
	id := s.Log(0, "scope entry", "")
	return service.EntryRef{ID: id, Expect: s.EntryExpect(id)}
}

func scopeBlock(s *testutil.Scenario) service.BlockRef {
	s.T.Helper()
	s.At("2026-10-28 08:00")
	id := s.Block(s.Add("scope block"), "09:00-11:00")
	s.At("2026-10-28 10:00")
	return service.BlockRef{BlockID: id, Expect: s.BlockExpect(id)}
}

func scopeSpan() domain.BlockSpan {
	return domain.BlockSpan{Date: testutil.D("10-28"), Start: 12 * 60, End: 13 * 60}
}

func scopeReopen(remove bool) func(*testutil.Scenario, service.Meta) error {
	return func(s *testutil.Scenario, m service.Meta) error {
		id := s.Add("completed task")
		s.Done(id)
		_, err := s.Svc.Reopen(s.Ctx(), m, service.ReopenIn{ID: id, Expect: s.Expect(id), RemoveEntry: remove})
		return err
	}
}

func scopeReversal(method string, required domain.Scopes) func(*testutil.Scenario, service.Meta) error {
	return func(s *testutil.Scenario, m service.Meta) error {
		// Create the target as the same identity before its grants change, so
		// missing-scope failures cannot pass merely because ownership differs.
		a := m.Actor
		a.Scopes = domain.AllScopes
		actor := s.As(a)
		switch required {
		case domain.ScopeCapture:
			actor.Log(0, "reverse capture", "")
		case domain.ScopeWrite:
			actor.Place(s.Add("reverse placement"), domain.PlaceKeep)
		case domain.ScopeDestroy:
			actor.Delete(s.Add("reverse deletion"))
		default:
			s.T.Fatalf("unsupported reversal fixture scopes: %s", required)
		}
		target := actor.Last.OpID
		switch method {
		case "Revert":
			_, err := s.Svc.Revert(s.Ctx(), m, service.RevertIn{OpID: target})
			return err
		case "Undo":
			_, err := s.Svc.Undo(s.Ctx(), m)
			return err
		case "Redo":
			actor.Must(actor.Svc.Undo(actor.Ctx(), actor.Meta()))
			_, err := s.Svc.Redo(s.Ctx(), m)
			return err
		default:
			s.T.Fatalf("unknown reversal method %s", method)
			return nil
		}
	}
}
