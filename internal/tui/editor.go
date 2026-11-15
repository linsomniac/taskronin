package tui

import (
	"context"
	"os"
	"os/exec"
	"slices"

	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/editor"
)

type editTarget struct {
	kind                       string // "task" or "entry"
	id                         int64
	etag, origTitle, origNotes string
	notesOnly                  bool
}

type editorTaskMsg struct {
	req    int
	id     int64
	detail *api.TaskDetail
	err    error
}

type editorSetupMsg struct {
	prefix string
	err    error
}

type editorDoneMsg struct {
	target editTarget
	draft  *editor.Draft
	buffer string // Read by the Exec callback, never by Update (CF13).
	err    error
}

type draftDiscardedMsg struct {
	path string
	err  error
}

func (a *App) editTask(id int64) tea.Cmd {
	if a.inflight {
		a.setBanner(bannerInfo, "busy…")
		return nil
	}
	// Reserve the mutation guard through the read, draft and editor stages so
	// repeated edit keys cannot open competing editors or strand a draft.
	a.inflight = true
	req, ctx, c := a.nextReq("editor"), a.ctx, a.api
	return func() tea.Msg {
		d, err := c.GetTask(ctx, id)
		return editorTaskMsg{req: req, id: id, detail: d, err: err}
	}
}

func (a *App) editEntry(e api.Entry) tea.Cmd {
	if a.inflight {
		a.setBanner(bannerInfo, "busy…")
		return nil
	}
	a.inflight = true
	return a.prepareEditor(editTarget{kind: "entry", id: e.ID, etag: e.ETag,
		origTitle: e.Title, origNotes: e.Notes, notesOnly: e.Kind == "complete"})
}

func (a *App) prepareEditor(target editTarget) tea.Cmd {
	env, ctx := a.cfg.Editor, a.ctx
	return func() tea.Msg {
		argv, err := editor.Command(env.Setting, env.Getenv)
		if err != nil {
			return editorSetupMsg{prefix: "bad editor setting: ", err: err}
		}
		buffer := editor.FormatTaskBuffer(target.origTitle, target.origNotes)
		if target.notesOnly {
			buffer = target.origNotes
		}
		d, err := (editor.Drafts{Dir: env.DraftsDir}).New(buffer)
		if err != nil {
			return editorSetupMsg{prefix: "cannot create draft: ", err: err}
		}
		cmd := exec.CommandContext(ctx, argv[0], append(slices.Clone(argv[1:]), d.Path)...) //nolint:gosec // G204: argv is the user's own editor setting, split without a shell (A§10.6)
		run := env.Exec(cmd, func(err error) tea.Msg {
			msg := editorDoneMsg{target: target, draft: d, err: err}
			if err == nil {
				b, readErr := os.ReadFile(d.Path)
				msg.buffer, msg.err = string(b), readErr
			}
			return msg
		})
		return run()
	}
}

func (a *App) editorDone(msg editorDoneMsg) tea.Cmd {
	a.inflight = false
	// AIDEV-NOTE: a draft is discarded only after the server confirms or when
	// nothing changed; every other outcome displays its path (A§9.6). Creation,
	// callback reads and Discard all run outside Update (CF13).
	if msg.err != nil {
		a.keepDraft(msg.draft.Path)
		a.setBanner(bannerError, "editor failed")
		return nil
	}
	target := msg.target
	title, notes := target.origTitle, msg.buffer
	if !target.notesOnly {
		var err error
		title, notes, err = editor.ParseTaskBuffer(msg.buffer)
		if err != nil {
			a.keepDraft(msg.draft.Path)
			a.setBanner(bannerError, "title required")
			return nil
		}
	}
	if title == target.origTitle && notes == target.origNotes {
		a.setBanner(bannerInfo, "no changes")
		return discardDraft(msg.draft)
	}
	var titlePatch, notesPatch api.Opt[string]
	if title != target.origTitle {
		titlePatch = api.Opt[string]{Set: true, V: title}
	}
	if notes != target.origNotes {
		notesPatch = api.Opt[string]{Set: true, V: notes}
	}
	return a.mutate("saved", func(ctx context.Context, c API) (*api.OpResponse, error) {
		m := client.Mut{IfMatch: target.etag}
		if target.kind == "task" {
			return c.UpdateTask(ctx, m, target.id, api.UpdateTaskRequest{Title: titlePatch, Notes: notesPatch})
		}
		return c.UpdateEntry(ctx, m, target.id, api.UpdateEntryRequest{Title: titlePatch, Notes: notesPatch})
	}, func(_ *App, _ *api.OpResponse) tea.Cmd {
		return discardDraft(msg.draft)
	}, func(a *App, _ error) {
		a.keepDraft(msg.draft.Path)
	})
}

func discardDraft(d *editor.Draft) tea.Cmd {
	return func() tea.Msg { return draftDiscardedMsg{path: d.Path, err: d.Discard()} }
}
