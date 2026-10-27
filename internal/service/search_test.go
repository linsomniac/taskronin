package service_test

import (
	"database/sql"
	"encoding/base64"
	"reflect"
	"testing"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/store"
	"taskronin/internal/testutil"
)

func TestFTSQuery(t *testing.T) {
	for in, want := range map[string]string{
		"foo bar":  `"foo" "bar"`,
		"fo*":      `"fo"*`,
		"fo**":     `"fo"*`,
		`a"b`:      `"a""b"`,
		"x * y":    `"x" "y"`,
		"NEAR(a b": `"NEAR(a" "b"`,
	} {
		if got, err := service.FTSQueryForTest(in); err != nil || got != want {
			t.Fatalf("ftsQuery(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "   ", "*", "** *"} {
		if _, err := service.FTSQueryForTest(in); err == nil {
			t.Fatalf("ftsQuery(%q) accepted", in)
		}
	}
}

func readSearch(t *testing.T, s *testutil.Scenario, in service.SearchIn) service.Page[service.SearchHit] {
	t.Helper()
	p, err := s.Svc.Search(s.Ctx(), s.Actor, in)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSearch(t *testing.T) {
	s := testutil.NewScenario(t)
	notesOnly := s.Add("ordinary", func(in *service.AddTaskIn) { in.Notes = "quartz" })
	title := s.Add("quartz", func(in *service.AddTaskIn) { in.Notes = "ordinary" })
	entry := s.Log(title, "quartz work", "10-27")
	standalone := s.Log(0, "quartz standalone", "10-28")
	deletedTask := s.Add("quartz deleted")
	retained := s.Log(deletedTask, "quartz retained work", "10-28")
	s.Delete(deletedTask)
	deletedEntry := s.Log(0, "quartz deleted entry", "10-28")
	s.DeleteEntry(deletedEntry)
	want := map[string]map[int64]service.SearchHit{
		"task": {
			int64(notesOnly): {Kind: "task", ID: int64(notesOnly), Title: "ordinary", Snippet: "quartz", Status: domain.StatusOpen},
			int64(title):     {Kind: "task", ID: int64(title), Title: "quartz", Snippet: "quartz", Status: domain.StatusOpen},
		},
		"entry": {
			int64(entry): {Kind: "entry", ID: int64(entry), TaskID: sql.Null[domain.TaskID]{V: title, Valid: true},
				Title: "quartz work", Snippet: "quartz work", WorkDate: sql.Null[civil.Date]{V: testutil.D("10-27"), Valid: true}},
			int64(standalone): {Kind: "entry", ID: int64(standalone), Title: "quartz standalone", Snippet: "quartz standalone",
				WorkDate: sql.Null[civil.Date]{V: testutil.D("10-28"), Valid: true}},
			int64(retained): {Kind: "entry", ID: int64(retained), TaskID: sql.Null[domain.TaskID]{V: deletedTask, Valid: true},
				Title: "quartz retained work", Snippet: "quartz retained work", WorkDate: sql.Null[civil.Date]{V: testutil.D("10-28"), Valid: true}},
		},
	}
	for _, kind := range []string{"", "all", "task", "entry"} {
		t.Run("kind="+kind, func(t *testing.T) {
			page := readSearch(t, s, service.SearchIn{Query: "quartz", Kind: kind})
			count := 5
			if kind == "task" || kind == "entry" {
				count = len(want[kind])
			}
			if len(page.Items) != count || page.NextCursor != "" {
				t.Fatalf("hits = %+v", page)
			}
			for _, hit := range page.Items {
				if expected, ok := want[hit.Kind][hit.ID]; !ok || hit != expected {
					t.Fatalf("unexpected hit = %+v; want %+v", hit, expected)
				}
			}
			if kind == "task" && page.Items[0].ID != int64(title) {
				t.Fatalf("title hit did not outrank notes-only hit: %+v", page.Items)
			}
		})
	}
	all := readSearch(t, s, service.SearchIn{Query: "quartz"})
	for _, limit := range []int{1, 2, 5} {
		var got []service.SearchHit
		in := service.SearchIn{Query: "quartz", Limit: limit}
		for pages := 0; ; pages++ {
			if pages > 5 {
				t.Fatal("paging did not terminate")
			}
			page := readSearch(t, s, in)
			if len(page.Items) > limit {
				t.Fatal("limit exceeded")
			}
			got = append(got, page.Items...)
			if page.NextCursor == "" {
				break
			}
			cursor, err := domain.DecodeCursor(page.NextCursor)
			if err != nil || cursor.Offset != len(got) || cursor.Keys != nil || cursor.ID != 0 {
				t.Fatalf("cursor = %+v, %v", cursor, err)
			}
			in.Cursor = page.NextCursor
		}
		if !reflect.DeepEqual(got, all.Items) {
			t.Fatalf("paged hits = %+v, want %+v", got, all.Items)
		}
	}
	page := readSearch(t, s, service.SearchIn{Query: "quartz", Kind: "task", Limit: 2})
	if len(page.Items) != 2 || page.NextCursor != "" {
		t.Fatalf("exact limit produced next cursor: %+v", page)
	}
	for _, in := range []service.SearchIn{
		{Query: "absent"},
		{Query: "quartz", Cursor: domain.EncodeCursor(domain.Cursor{Offset: 100})},
	} {
		page := readSearch(t, s, in)
		if page.Items == nil || len(page.Items) != 0 || page.NextCursor != "" {
			t.Fatalf("empty page = %+v", page)
		}
	}
	prefix := readSearch(t, s, service.SearchIn{Query: "quar**"})
	if !reflect.DeepEqual(prefix.Items, all.Items) {
		t.Fatal("prefix query did not match")
	}
}

func TestSearchValidationAndRead(t *testing.T) {
	s := testutil.NewScenario(t)
	id := s.Add("surface", testutil.DeferTo("10-29"))
	before, version := s.OpCount(), s.Task(id).Version
	s.At("10-29 00:00")
	for _, in := range []service.SearchIn{
		{}, {Query: "** *"}, {Query: "x", Kind: "tasks"}, {Query: "x", Limit: -1}, {Query: "x", Limit: 501},
	} {
		_, err := s.Svc.Search(s.Ctx(), s.Actor, in)
		testutil.RequireCode(t, err, domain.CodeValidation)
	}
	for _, cursor := range []string{
		"garbage", domain.EncodeCursor(domain.Cursor{Offset: -1}),
		domain.EncodeCursor(domain.Cursor{Keys: []any{1}, ID: 1}),
		base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"k":null,"id":0}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"v":2,"o":0}`)),
	} {
		_, err := s.Svc.Search(s.Ctx(), s.Actor, service.SearchIn{Query: "x", Cursor: cursor})
		testutil.RequireCode(t, err, domain.CodeMalformed)
	}
	_, err := s.Svc.Search(s.Ctx(), testutil.TokenActor("capture", domain.ScopeCapture), service.SearchIn{Query: "surface"})
	testutil.RequireCode(t, err, domain.CodeForbidden)
	page, err := s.Svc.Search(s.Ctx(), testutil.TokenActor("reader", domain.ScopeRead), service.SearchIn{Query: "surface"})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != int64(id) {
		t.Fatalf("read-only search = %+v, %v", page, err)
	}
	if s.OpCount() != before || s.Task(id).Version != version {
		t.Fatal("search reconciled or wrote state")
	}
}

func FuzzFTSQuery(f *testing.F) {
	for _, seed := range []string{"foo bar", "fo*", "fo**", `a"b`, "x * y", "NEAR(a b", "", "   ", "*", "** *", "\x00", "a\x00b", "\xff", "é\u2003猫", `"`, "title:x OR (y NOT z)", "a*b"} {
		f.Add(seed)
	}
	st := testutil.Store(f)
	f.Fuzz(func(t *testing.T, user string) {
		query, err := service.FTSQueryForTest(user)
		if err != nil {
			testutil.RequireCode(t, err, domain.CodeValidation)
			return
		}
		if _, err := store.Search(t.Context(), st.Reader, query, "all", 10, 0); err != nil {
			t.Fatalf("ftsQuery(%q) = %q rejected by FTS5: %v", user, query, err)
		}
	})
}
