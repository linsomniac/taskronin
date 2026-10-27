package service_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

func isCode(err error, code domain.Code) bool {
	var problem *domain.Error
	return errors.As(err, &problem) && problem.Code == code
}

func TestIDM01(t *testing.T) {
	s := testutil.NewScenario(t)
	m := s.MetaWith("key-K-0123456789ab", `{"title":"A"}`)
	first := s.Must(s.Svc.AddTask(s.Ctx(), m, service.AddTaskIn{Title: "A"}))
	title := "A2"
	s.Update(first.Task.ID, func(in *service.UpdateTaskIn) { in.Title = &title })
	before, count := s.Task(first.Task.ID), s.OpCount()
	replay := s.Must(s.Svc.AddTask(s.Ctx(), m, service.AddTaskIn{Title: "A"}))
	if !replay.Replayed || !replay.Created || replay.OpID != first.OpID || replay.Task == nil ||
		!reflect.DeepEqual(*replay.Task, before) || replay.Task.Title != title || s.OpCount() != count {
		t.Fatalf("add replay after update = %+v", replay)
	}
	var tasks int
	if err := s.Store.Reader.QueryRowContext(s.Ctx(), `SELECT COUNT(*) FROM tasks`).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	row, err := store.GetIdem(s.Ctx(), s.Store.Reader, m.Actor.Key(), m.Idem.Key)
	if err != nil || row.Status != 201 || tasks != 1 {
		t.Fatalf("replay row = %+v, tasks = %d, err = %v", row, tasks, err)
	}
}

func TestUpdateTaskFields(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A", testutil.DeferTo("11-02"))
	before := s.Task(id)
	title, notes, project, priority := " B ", "a\r\nb", "infra.keycloak", domain.PriorityH
	s.As(testutil.AI()).Update(id, func(in *service.UpdateTaskIn) {
		in.Title, in.Notes, in.Project, in.Priority = &title, &notes, &project, &priority
		in.TagsAdd = []string{"zeta", "Alpha"}
	})
	v := s.Task(id)
	if v.Title != "B" || v.Notes != "a\nb" || v.Project != project || v.Priority != priority ||
		!slices.Equal(v.Tags, []string{"Alpha", "zeta"}) || v.Version != before.Version+1 || v.DeferDate != before.DeferDate || !v.UpdatedAt.Equal(s.Clock.Now()) {
		t.Fatalf("updated task = %+v", v)
	}
	requireNotPlaced(t, s, before)
	op := latestOp(t, s)
	if op.Kind != domain.OpTaskUpdate || op.Scopes != domain.ScopeWrite || !op.Revertible || !slices.Contains(s.Stack(testutil.AI().Key(), store.StackUndo), op.ID) {
		t.Fatalf("update op = %+v", op)
	}
	requireEventKeys(t, s, op.ID, "task:update 1", "task_tags:set 1")
	var changed map[string]any
	if err := json.Unmarshal(s.Events(op.ID)[0].After, &changed); err != nil {
		t.Fatal(err)
	}
	if len(changed) != 4 || changed["title"] != "B" || changed["notes"] != "a\nb" || changed["project"] != project || changed["priority"] != string(priority) {
		t.Fatalf("field event = %v", changed)
	}
	before = v
	empty, none := "", domain.PriorityNone
	s.Update(id, func(in *service.UpdateTaskIn) { in.Notes, in.Project, in.Priority = &empty, &empty, &none })
	v = s.Task(id)
	if v.Title != "B" || v.Notes != "" || v.Project != "" || v.Priority != domain.PriorityNone || v.Version != before.Version+1 || !slices.Equal(v.Tags, before.Tags) {
		t.Fatalf("cleared task fields = %+v", v)
	}
	requireTaskOp(t, s, id, domain.OpTaskUpdate, domain.ScopeWrite)
	requireEventKeys(t, s, s.Last.OpID, "task:update 1")
}

func TestUpdateTaskTagsAndProjects(t *testing.T) {
	s := testutil.NewScenario(t)
	a, b := s.Add("A"), s.Add("B")
	exp := s.Expect(a)
	s.Update(a, func(in *service.UpdateTaskIn) { in.TagsAdd = []string{"Infra"} })
	if s.Task(a).Version != exp.Version+1 {
		t.Fatal("tag change did not bump task version")
	}
	_, err := s.Svc.UpdateTask(s.Ctx(), s.Meta(), service.UpdateTaskIn{ID: a, Expect: exp})
	testutil.RequireCode(t, err, domain.CodePrecondition)
	s.Update(b, func(in *service.UpdateTaskIn) { in.TagsAdd = []string{"infra"} })
	var count int
	if err := s.Store.Reader.QueryRowContext(s.Ctx(), `SELECT COUNT(*) FROM tags`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 || !slices.Equal(s.Task(a).Tags, []string{"Infra"}) || !slices.Equal(s.Task(b).Tags, []string{"Infra"}) {
		t.Fatalf("case-insensitive tags = %v / %v; %d tag rows", s.Task(a).Tags, s.Task(b).Tags, count)
	}
	for _, mod := range []func(*service.UpdateTaskIn){
		func(in *service.UpdateTaskIn) { in.TagsRemove = []string{"nope"} },
		func(in *service.UpdateTaskIn) { in.TagsAdd = []string{"INFRA", "infra"} },
	} {
		before, ops, stack := s.Task(a), s.OpCount(), s.Stack(s.Actor.Key(), store.StackUndo)
		s.Update(a, mod)
		if !s.Last.NoChange || !s.Last.OpID.IsZero() || !reflect.DeepEqual(s.Task(a), before) || s.OpCount() != ops || !slices.Equal(s.Stack(s.Actor.Key(), store.StackUndo), stack) {
			t.Fatalf("no-op tag update = %+v", s.Last)
		}
	}
	s.Update(a, func(in *service.UpdateTaskIn) { in.TagsAdd = []string{"zeta", "alpha", "Beta", "ALPHA"} })
	e := s.Events(s.Last.OpID)[0]
	if string(e.Before) != `{"tags":["Infra"]}` || string(e.After) != `{"tags":["alpha","Beta","Infra","zeta"]}` {
		t.Fatalf("sorted tag images = %s / %s", e.Before, e.After)
	}
	s.Update(a, func(in *service.UpdateTaskIn) { in.TagsRemove = []string{"INFRA", "ZETA", "ALPHA", "beta"} })
	e = s.Events(s.Last.OpID)[0]
	if string(e.After) != `{"tags":[]}` || len(s.Task(a).Tags) != 0 {
		t.Fatalf("cleared tag event = %s", e.After)
	}

	for _, tc := range []struct {
		name, field, value string
		mod                func(*service.UpdateTaskIn, *string)
	}{
		{"tag_space", "tags", "a b", func(in *service.UpdateTaskIn, v *string) { in.TagsAdd = []string{*v} }},
		{"remove_space", "tags", "a b", func(in *service.UpdateTaskIn, v *string) { in.TagsRemove = []string{*v} }},
		{"tag_long", "tags", strings.Repeat("a", 65), func(in *service.UpdateTaskIn, v *string) { in.TagsAdd = []string{*v} }},
		{"tag_empty", "tags", "", func(in *service.UpdateTaskIn, v *string) { in.TagsAdd = []string{*v} }},
		{"project_space", "project", "infra keycloak", func(in *service.UpdateTaskIn, v *string) { in.Project = v }},
		{"project_long", "project", strings.Repeat("a", 101), func(in *service.UpdateTaskIn, v *string) { in.Project = v }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, ops := s.Task(a), s.OpCount()
			title := "must roll back"
			in := service.UpdateTaskIn{ID: a, Expect: s.Expect(a), Title: &title}
			tc.mod(&in, &tc.value)
			_, err := s.Svc.UpdateTask(s.Ctx(), s.Meta(), in)
			var problem *domain.Error
			if !errors.As(err, &problem) || problem.Code != domain.CodeValidation || !slices.ContainsFunc(problem.Fields, func(f domain.FieldError) bool { return f.Field == tc.field && f.Message != "" }) {
				t.Fatalf("%s = %v, want validation naming %s", tc.name, err, tc.field)
			}
			if !reflect.DeepEqual(s.Task(a), before) || s.OpCount() != ops {
				t.Fatal("validation wrote partial changes")
			}
		})
	}
	project := strings.Repeat("a", 100)
	s.Update(a, func(in *service.UpdateTaskIn) { in.Project = &project; in.TagsAdd = []string{strings.Repeat("a", 64)} })
	if s.Task(a).Project != project || len(s.Task(a).Tags[0]) != 64 {
		t.Fatal("valid name boundaries rejected")
	}
}

func TestUpdateTaskNoChangeAndReplay(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	before, count, stack := s.Task(id), s.OpCount(), s.Stack(s.Actor.Key(), store.StackUndo)
	title, notes, project, priority := " A ", "", "", domain.PriorityNone
	m := s.Meta()
	in := service.UpdateTaskIn{ID: id, Expect: s.Expect(id), Title: &title, Notes: &notes, Project: &project, Priority: &priority}
	first := s.Must(s.Svc.UpdateTask(s.Ctx(), m, in))
	if !first.NoChange || !first.OpID.IsZero() || first.Task == nil || first.Task.ID != id || len(first.Effects) != 0 ||
		!reflect.DeepEqual(s.Task(id), before) || s.OpCount() != count || !slices.Equal(s.Stack(s.Actor.Key(), store.StackUndo), stack) {
		t.Fatalf("unchanged update = %+v", first)
	}
	title = "B"
	s.Update(id, func(in *service.UpdateTaskIn) { in.Title = &title })
	count = s.OpCount()
	replay := s.Must(s.Svc.UpdateTask(s.Ctx(), m, in))
	if !replay.Replayed || !replay.NoChange || !replay.OpID.IsZero() || replay.Task.Title != "B" || s.OpCount() != count {
		t.Fatalf("no-op replay = %+v", replay)
	}
	m = s.Meta()
	title = "C"
	in = service.UpdateTaskIn{ID: id, Expect: s.Expect(id), Title: &title}
	first = s.Must(s.Svc.UpdateTask(s.Ctx(), m, in))
	title = "D"
	s.Update(id, func(in *service.UpdateTaskIn) { in.Title = &title })
	count = s.OpCount()
	replay = s.Must(s.Svc.UpdateTask(s.Ctx(), m, in))
	if !replay.Replayed || replay.NoChange || replay.OpID != first.OpID || replay.Task.Title != "D" || s.OpCount() != count {
		t.Fatalf("update replay = %+v", replay)
	}
}

func TestUpdateTaskPreconditionsAndScope(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	_, err := s.Svc.UpdateTask(s.Ctx(), s.Meta(), service.UpdateTaskIn{ID: id})
	testutil.RequireCode(t, err, domain.CodePreconditionReq)
	_, err = s.Svc.UpdateTask(s.Ctx(), s.Meta(), service.UpdateTaskIn{ID: id, Expect: domain.Expect{Gen: s.Svc.Generation(), Version: 99}})
	testutil.RequireCode(t, err, domain.CodePrecondition)
	_, err = s.Svc.UpdateTask(s.Ctx(), s.Meta(), service.UpdateTaskIn{ID: 999, Expect: s.Expect(id)})
	testutil.RequireCode(t, err, domain.CodeNotFound)
	_, err = s.Svc.UpdateTask(s.Ctx(), s.As(testutil.TokenActor("capture", domain.ScopeCapture)).Meta(), service.UpdateTaskIn{ID: id, Expect: s.Expect(id)})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	bad := domain.Priority("invalid")
	_, err = s.Svc.UpdateTask(s.Ctx(), s.Meta(), service.UpdateTaskIn{ID: id, Expect: s.Expect(id), Priority: &bad})
	testutil.RequireCode(t, err, domain.CodeValidation)
	for _, closeTask := range []func(*testutil.Scenario, domain.TaskID){func(s *testutil.Scenario, id domain.TaskID) { s.Done(id) }, (*testutil.Scenario).Drop} {
		next := s.Add("closed")
		closeTask(s, next)
		title := "edited closed"
		s.Update(next, func(in *service.UpdateTaskIn) { in.Title = &title })
		if s.Task(next).Title != title {
			t.Fatal("closed live task cannot be edited")
		}
	}
}

type textInputCall func(string) (string, error)

func titleInputs(s *testutil.Scenario, id domain.TaskID, entry domain.EntryID) map[string]textInputCall {
	return map[string]textInputCall{
		"AddTask.title": func(v string) (string, error) {
			r, err := s.Svc.AddTask(s.Ctx(), s.Meta(), service.AddTaskIn{Title: v})
			if err != nil {
				return "", err
			}
			return r.Task.Title, nil
		},
		"UpdateTask.title": func(v string) (string, error) {
			r, err := s.Svc.UpdateTask(s.Ctx(), s.Meta(), service.UpdateTaskIn{ID: id, Expect: s.Expect(id), Title: &v})
			if err != nil {
				return "", err
			}
			return r.Task.Title, nil
		},
		"Complete.As": func(v string) (string, error) {
			r, err := s.Svc.Complete(s.Ctx(), s.Meta(), service.CompleteIn{ID: id, Expect: s.Expect(id), As: &v})
			if err != nil {
				return "", err
			}
			return r.Task.Title, nil
		},
		"Complete.LogTitle": func(v string) (string, error) {
			r, err := s.Svc.Complete(s.Ctx(), s.Meta(), service.CompleteIn{ID: id, Expect: s.Expect(id), LogTitle: v})
			if err != nil {
				return "", err
			}
			return r.Entry.Title, nil
		},
		"AddEntry.title": func(v string) (string, error) {
			r, err := s.Svc.AddEntry(s.Ctx(), s.Meta(), service.AddEntryIn{Title: v})
			if err != nil {
				return "", err
			}
			return r.Entry.Title, nil
		},
		"UpdateEntry.title": func(v string) (string, error) {
			r, err := s.Svc.UpdateEntry(s.Ctx(), s.Meta(), service.UpdateEntryIn{ID: entry, Expect: s.EntryExpect(entry), Title: &v})
			if err != nil {
				return "", err
			}
			return r.Entry.Title, nil
		},
	}
}

func notesInputs(s *testutil.Scenario, id domain.TaskID, entry domain.EntryID) map[string]textInputCall {
	return map[string]textInputCall{
		"AddTask.notes": func(v string) (string, error) {
			r, err := s.Svc.AddTask(s.Ctx(), s.Meta(), service.AddTaskIn{Title: "ok", Notes: v})
			if err != nil {
				return "", err
			}
			return r.Task.Notes, nil
		},
		"UpdateTask.notes": func(v string) (string, error) {
			r, err := s.Svc.UpdateTask(s.Ctx(), s.Meta(), service.UpdateTaskIn{ID: id, Expect: s.Expect(id), Notes: &v})
			if err != nil {
				return "", err
			}
			return r.Task.Notes, nil
		},
		"Complete.LogNotes": func(v string) (string, error) {
			r, err := s.Svc.Complete(s.Ctx(), s.Meta(), service.CompleteIn{ID: id, Expect: s.Expect(id), LogNotes: v})
			if err != nil {
				return "", err
			}
			return r.Entry.Notes, nil
		},
		"AddEntry.notes": func(v string) (string, error) {
			r, err := s.Svc.AddEntry(s.Ctx(), s.Meta(), service.AddEntryIn{Title: "ok", Notes: v})
			if err != nil {
				return "", err
			}
			return r.Entry.Notes, nil
		},
		"UpdateEntry.notes": func(v string) (string, error) {
			r, err := s.Svc.UpdateEntry(s.Ctx(), s.Meta(), service.UpdateEntryIn{ID: entry, Expect: s.EntryExpect(entry), Notes: &v})
			if err != nil {
				return "", err
			}
			return r.Entry.Notes, nil
		},
	}
}

func TestTextInputsEveryOp(t *testing.T) {
	bad := map[string]string{"bidi": "a\u202eb", "c1": "a\u0085b", "utf8": "\xff", "blank": "  \t ", "embedded_bom": "a\ufeffb"}
	for name, v := range bad {
		t.Run(name, func(t *testing.T) {
			s := testutil.NewScenario(t)
			id := s.Add("ok")
			e := s.Log(id, "ok", "")
			before, count := s.Task(id), s.OpCount()
			for op, call := range titleInputs(s, id, e) {
				if _, err := call(v); !isCode(err, domain.CodeValidation) {
					t.Fatalf("%s(%q) = %v, want validation", op, v, err)
				}
			}
			if name != "blank" {
				for op, call := range notesInputs(s, id, e) {
					if _, err := call(v); !isCode(err, domain.CodeValidation) {
						t.Fatalf("%s(%q) = %v, want validation", op, v, err)
					}
				}
			}
			if !reflect.DeepEqual(s.Task(id), before) || s.OpCount() != count {
				t.Fatal("invalid text changed task or recorded an op")
			}
		})
	}
	for _, tc := range []struct {
		name, input, want string
		invalid           bool
		factory           func(*testutil.Scenario, domain.TaskID, domain.EntryID) map[string]textInputCall
	}{
		{"title_500_runes", strings.Repeat("é", 500), strings.Repeat("é", 500), false, titleInputs},
		{"title_501_runes", strings.Repeat("é", 501), "", true, titleInputs},
		{"title_leading_bom", "\ufeff é ", "é", false, titleInputs},
		{"notes_crlf", "a\r\nb", "a\nb", false, notesInputs},
		{"notes_leading_bom", "\ufeffa\r\nb", "a\nb", false, notesInputs},
		{"notes_65536_bytes", strings.Repeat("é", 32768), strings.Repeat("é", 32768), false, notesInputs},
		{"notes_65537_bytes", strings.Repeat("é", 32768) + "x", "", true, notesInputs},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for op := range tc.factory(nil, 0, 0) {
				t.Run(op, func(t *testing.T) {
					s := testutil.NewScenario(t)
					id := s.Add("ok")
					e := s.Log(id, "ok", "")
					got, err := tc.factory(s, id, e)[op](tc.input)
					if tc.invalid {
						testutil.RequireCode(t, err, domain.CodeValidation)
					} else if err != nil || got != tc.want {
						t.Fatalf("%s: stored text length = %d, want %d; err = %v", op, len(got), len(tc.want), err)
					}
				})
			}
		})
	}
}

func TestUndeletePurgedTask(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("A")
	s.Delete(id)
	// AIDEV-NOTE: F14 fixture shortcut: offline purge arrives in Task 37.
	s.Must(s.Svc.TestOpRaw(s.Ctx(), s.Meta(), []domain.EntityRef{{Entity: domain.EntTask, ID: int64(id)}}, func(q store.Q) error {
		_, err := q.ExecContext(s.Ctx(), `UPDATE tasks SET purged_at = ? WHERE id = ?`, s.Clock.Now().UnixMilli(), id)
		return err
	}))
	before, count := s.Task(id), s.OpCount()
	_, err := s.Svc.Undelete(s.Ctx(), s.Meta(), service.TaskRef{ID: id, Expect: s.Expect(id)})
	testutil.RequireCode(t, err, domain.CodeConflict)
	title := "cannot restore purged content"
	_, err = s.Svc.UpdateTask(s.Ctx(), s.Meta(), service.UpdateTaskIn{ID: id, Expect: s.Expect(id), Title: &title})
	testutil.RequireCode(t, err, domain.CodeConflict)
	if !reflect.DeepEqual(s.Task(id), before) || s.OpCount() != count {
		t.Fatal("purged task was restored")
	}
}
