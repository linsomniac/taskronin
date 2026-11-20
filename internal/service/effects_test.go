package service_test

import (
	"database/sql"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"testing"

	"taskronin/internal/domain"
	"taskronin/internal/service"
)

func TestEffectKindsMatchDeclarations(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "effects.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	declared := make(map[service.EffectKind]bool)
	for node := range ast.Preorder(file) {
		spec, ok := node.(*ast.ValueSpec)
		if !ok {
			continue
		}
		typ, ok := spec.Type.(*ast.Ident)
		if !ok || typ.Name != "EffectKind" {
			continue
		}
		for _, expr := range spec.Values {
			lit, ok := expr.(*ast.BasicLit)
			if !ok {
				t.Fatal("expected string literal for EffectKind", spec.Names)
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			declared[service.EffectKind(value)] = true
		}
	}
	for _, kind := range service.EffectKinds() {
		if !declared[kind] {
			t.Fatalf("duplicate or undeclared EffectKind %q", kind)
		}
		delete(declared, kind)
	}
	if len(declared) != 0 {
		t.Errorf("EffectKinds is missing %v", declared)
	}
}

func TestEffectsFromEventsRevertedCarriesRoot(t *testing.T) {
	undoID := domain.NewOpID()
	rec := domain.OpRecord{Via: "redo", RevertsOpID: sql.Null[domain.OpID]{V: undoID, Valid: true}}
	root := &service.Effect{Kind: service.EffReverted, Via: "redo", From: domain.NewOpID().String()[:8], To: "task.complete"}
	evs := []domain.Event{{Entity: domain.EntTask, Action: domain.ActUpdate,
		Before: json.RawMessage(`{"status":"done"}`), After: json.RawMessage(`{"status":"open"}`)}}
	for _, tt := range []struct {
		root *service.Effect
		want service.Effect
	}{
		{root, *root},
		{nil, service.Effect{Kind: service.EffReverted, Via: "redo", From: undoID.String()[:8]}},
	} {
		got := service.EffectsFromEventsForTest(rec, tt.root, evs)
		if len(got) == 0 || got[0] != tt.want {
			t.Errorf("effects = %+v, want first effect %+v", got, tt.want)
		}
	}
}

func TestEffectsFromEvents(t *testing.T) {
	id := domain.NewOpID()
	undo := domain.OpRecord{Kind: domain.OpUndo, Via: "undo", RevertsOpID: sql.Null[domain.OpID]{V: id, Valid: true}}
	for _, tt := range []struct {
		name          string
		entity        domain.EntityKind
		action        domain.Action
		before, after string
		want          []service.Effect
	}{
		{"insert", domain.EntTask, domain.ActInsert, ``, `{"title":"A"}`, []service.Effect{{Kind: service.EffCreated, TaskID: 7}}},
		{"title", domain.EntTask, domain.ActUpdate, `{"title":"A"}`, `{"title":"B"}`, []service.Effect{{Kind: service.EffRetitled, TaskID: 7, From: "A", To: "B"}}},
		{"complete", domain.EntTask, domain.ActUpdate, `{"status":"open"}`, `{"status":"done","completed_at":1,"completed_date":"2026-10-28"}`, []service.Effect{{Kind: service.EffCompleted, TaskID: 7}}},
		{"drop", domain.EntTask, domain.ActUpdate, `{"status":"open"}`, `{"status":"dropped"}`, []service.Effect{{Kind: service.EffDropped, TaskID: 7}}},
		{"reopen", domain.EntTask, domain.ActUpdate, `{"status":"done"}`, `{"status":"open","completed_at":null}`, []service.Effect{{Kind: service.EffReopened, TaskID: 7}}},
		{"place", domain.EntTask, domain.ActUpdate, `{"last_placed_seq":null}`, `{"last_placed_at":1,"last_placed_seq":2,"due_ack":"none","due_ack_date":"2026-10-28","updated_at":1,"version":2}`, []service.Effect{{Kind: service.EffPlaced, TaskID: 7}}},
		{"defer", domain.EntTask, domain.ActUpdate, `{"defer_date":null}`, `{"defer_date":"2026-10-30","defer_min":540}`, []service.Effect{{Kind: service.EffDeferred, TaskID: 7, To: "2026-10-30 09:00"}}},
		{"undefer", domain.EntTask, domain.ActUpdate, `{"defer_date":"2026-10-30"}`, `{"defer_date":null,"defer_min":null}`, []service.Effect{{Kind: service.EffUndeferred, TaskID: 7}}},
		{"due", domain.EntTask, domain.ActUpdate, `{"due_date":null}`, `{"due_date":"2026-10-30","due_min":540,"due_ack":"none"}`, []service.Effect{{Kind: service.EffDueSet, TaskID: 7, To: "2026-10-30 09:00"}}},
		{"clear due", domain.EntTask, domain.ActUpdate, `{"due_date":"2026-10-30"}`, `{"due_date":null,"due_min":null}`, []service.Effect{{Kind: service.EffDueCleared, TaskID: 7}}},
		{"other fields", domain.EntTask, domain.ActUpdate, `{"notes":"","project":null}`, `{"notes":"note","project":"work"}`, []service.Effect{{Kind: service.EffUpdated, TaskID: 7}}},
		{"move", domain.EntTask, domain.ActMove, ``, ``, []service.Effect{{Kind: service.EffMoved, TaskID: 7}}},
		{"delete", domain.EntTask, domain.ActTombstone, ``, ``, []service.Effect{{Kind: service.EffDeleted, TaskID: 7}}},
		{"undelete", domain.EntTask, domain.ActUntombstone, ``, ``, []service.Effect{{Kind: service.EffUndeleted, TaskID: 7}}},
		{"tags", domain.EntTaskTags, domain.ActSet, ``, `{"tags":["work"]}`, []service.Effect{{Kind: service.EffUpdated, TaskID: 7}}},
		{"reason add", domain.EntTriage, domain.ActAdd, ``, `{"reason":"new"}`, []service.Effect{{Kind: service.EffReasonAdded, TaskID: 7, Reason: domain.ReasonNew}}},
		{"reason remove", domain.EntTriage, domain.ActRemove, `{"reason":"new"}`, ``, []service.Effect{{Kind: service.EffReasonRemoved, TaskID: 7, Reason: domain.ReasonNew}}},
		{"block", domain.EntBlock, domain.ActInsert, ``, `{"date":"2026-10-28","start_min":600,"end_min":660}`, []service.Effect{{Kind: service.EffBlocked, TaskID: 7, BlockID: 9, To: "2026-10-28 10:00-11:00"}}},
		{"block move", domain.EntBlock, domain.ActUpdate, `{"start_min":600}`, `{"start_min":630}`, []service.Effect{{Kind: service.EffBlockMoved, TaskID: 7, BlockID: 9}}},
		{"cancel", domain.EntBlock, domain.ActUpdate, `{"status":"planned"}`, `{"status":"cancelled"}`, []service.Effect{{Kind: service.EffBlockCancelled, TaskID: 7, BlockID: 9}}},
		{"block delete", domain.EntBlock, domain.ActUpdate, `{"deleted_at":null}`, `{"deleted_at":1}`, []service.Effect{{Kind: service.EffBlockCancelled, TaskID: 7, BlockID: 9}}},
		{"outcome", domain.EntBlock, domain.ActUpdate, `{"status":"planned"}`, `{"status":"done","outcome_source":"user"}`, []service.Effect{{Kind: service.EffOutcomeSet, TaskID: 7, BlockID: 9, To: "done"}}},
		{"resolve", domain.EntBlock, domain.ActUpdate, `{"status":"planned"}`, `{"status":"worked","outcome_source":"auto"}`, []service.Effect{{Kind: service.EffBlockResolved, TaskID: 7, BlockID: 9, To: "worked"}}},
		{"slip", domain.EntBlock, domain.ActUpdate, `{"slipped":0}`, `{"slipped":1}`, []service.Effect{{Kind: service.EffSlipped, TaskID: 7, BlockID: 9}}},
		{"unslip", domain.EntBlock, domain.ActUpdate, `{"slipped":1}`, `{"slipped":0}`, []service.Effect{{Kind: service.EffUnslipped, TaskID: 7, BlockID: 9}}},
		{"entry", domain.EntEntry, domain.ActInsert, ``, ``, []service.Effect{{Kind: service.EffEntryLogged, TaskID: 7, EntryID: 9}}},
		{"entry delete", domain.EntEntry, domain.ActTombstone, ``, ``, []service.Effect{{Kind: service.EffEntryDeleted, TaskID: 7, EntryID: 9}}},
		{"entry restore", domain.EntEntry, domain.ActUntombstone, ``, ``, []service.Effect{{Kind: service.EffEntryRestored, TaskID: 7, EntryID: 9}}},
		{"entry relink", domain.EntEntry, domain.ActUpdate, `{"task_id":3}`, `{"task_id":7}`, []service.Effect{{Kind: service.EffEntryRelinked, TaskID: 7, EntryID: 9}}},
		{"entry update", domain.EntEntry, domain.ActUpdate, `{"notes":""}`, `{"notes":"note"}`, []service.Effect{{Kind: service.EffEntryUpdated, TaskID: 7, EntryID: 9}}},
		{"focus", domain.EntFocus, domain.ActSet, `{"task_id":null}`, `{"task_id":7}`, []service.Effect{{Kind: service.EffFocusSet, TaskID: 7}}},
		{"clear focus", domain.EntFocus, domain.ActSet, `{"task_id":7}`, `{"task_id":null}`, []service.Effect{{Kind: service.EffFocusCleared, TaskID: 7}}},
		{"interrupt", domain.EntInterruption, domain.ActInsert, ``, `{"from_task_id":3,"to_task_id":7}`, []service.Effect{{Kind: service.EffInterrupted, TaskID: 7, From: "3"}}},
		{"purge", domain.EntTask, domain.ActPurge, ``, `{"ids":[7]}`, []service.Effect{{Kind: service.EffPurged, TaskID: 7}}},
		{"setting", domain.EntSetting, domain.ActUpdate, ``, ``, []service.Effect{}},
		{"token", domain.EntToken, domain.ActInsert, ``, ``, []service.Effect{}},
		{"auth", domain.EntAuth, domain.ActUpdate, ``, ``, []service.Effect{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ev := domain.Event{Entity: tt.entity, EntityID: 9, TaskID: sql.Null[domain.TaskID]{V: 7, Valid: true}, Action: tt.action, Before: json.RawMessage(tt.before), After: json.RawMessage(tt.after)}
			if got := service.EffectsFromEventsForTest(domain.OpRecord{}, nil, []domain.Event{ev}); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("effects = %+v, want %+v", got, tt.want)
			}
		})
	}
	t.Run("undo without events", func(t *testing.T) {
		if got := service.EffectsFromEventsForTest(undo, nil, nil); got == nil || len(got) != 0 {
			t.Fatalf("effects = %+v, want []", got)
		}
	})
	t.Run("undo with focus", func(t *testing.T) {
		evs := []domain.Event{{Entity: domain.EntFocus, Action: domain.ActSet, TaskID: sql.Null[domain.TaskID]{V: 7, Valid: true}, After: json.RawMessage(`{"task_id":7}`)}}
		want := []service.Effect{{Kind: service.EffReverted, Via: "undo", From: id.String()[:8]}, {Kind: service.EffFocusSet, TaskID: 7}}
		if got := service.EffectsFromEventsForTest(undo, nil, evs); !reflect.DeepEqual(got, want) {
			t.Fatalf("effects = %+v, want %+v", got, want)
		}
	})
	t.Run("review once", func(t *testing.T) {
		got := service.EffectsFromEventsForTest(domain.OpRecord{Kind: domain.OpAIReview}, nil, []domain.Event{{Entity: domain.EntSetting}, {Entity: domain.EntSetting, Action: domain.ActUpdate, After: json.RawMessage(`{"key":"ai_reviewed_through","value":"123"}`)}})
		if !reflect.DeepEqual(got, []service.Effect{{Kind: service.EffReviewed, To: "123"}}) {
			t.Fatalf("effects = %+v", got)
		}
	})
}
