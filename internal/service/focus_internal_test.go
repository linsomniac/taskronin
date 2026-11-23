package service

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/store"
)

func TestClearFocusIf(t *testing.T) {
	s, m := runnerFixture(t)
	task := fixtureTask(t, s, 1)
	if _, err := s.SetFocus(t.Context(), m, SetFocusIn{TaskID: sql.Null[domain.TaskID]{V: task.ID, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.TaskID{task.ID + 1, task.ID, task.ID} {
		before, err := store.GetFocus(t.Context(), s.store.Reader)
		if err != nil {
			t.Fatal(err)
		}
		clears := before.TaskID.Valid && before.TaskID.V == id
		r, err := s.run(t.Context(), m, opSpec{kind: "test.clear_focus", class: classForward}, func(o *op) error {
			return o.clearFocusIf(id)
		})
		if err != nil {
			t.Fatal(err)
		}
		after, err := store.GetFocus(t.Context(), s.store.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if clears {
			if r.NoChange || after.TaskID.Valid || after.Date.Valid || after.Version != before.Version+1 {
				t.Fatalf("matching focus was not cleared: %+v", after)
			}
		} else if !r.NoChange || !reflect.DeepEqual(before, after) {
			t.Fatalf("nonmatching/already clear focus changed: %+v -> %+v", before, after)
		}
	}
}
