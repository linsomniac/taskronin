package server

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"

	"taskronin/internal/api"
	"taskronin/internal/buildinfo"
	"taskronin/internal/domain"
	"taskronin/internal/service"
)

func (s *Server) opResponse(res *service.OpResult) api.OpResponse {
	out := api.OpResponse{Replayed: res.Replayed, Effects: make([]api.Effect, len(res.Effects)), Warnings: append([]string{}, res.Warnings...)}
	if !res.OpID.IsZero() {
		out.OpID = new(res.OpID.String())
	}
	if res.Task != nil {
		out.Task = new(s.conv.Task(*res.Task))
	}
	if res.Entry != nil {
		out.Entry = new(s.conv.Entry(*res.Entry))
	}
	if res.Block != nil {
		out.Block = new(s.conv.Block(*res.Block))
	}
	if res.Tasks != nil {
		out.Tasks = make([]api.Task, len(res.Tasks))
		for i, task := range res.Tasks {
			out.Tasks[i] = s.conv.Task(task)
		}
	}
	for i, e := range res.Effects {
		out.Effects[i] = effectResponse(e)
	}
	return out
}

func (s *Server) currentResponse(v any) (json.RawMessage, error) {
	var wire any
	switch v := v.(type) {
	case domain.TaskView:
		wire = s.conv.Task(v)
	case *domain.TaskView:
		wire = s.conv.Task(*v)
	case domain.EntryView:
		wire = s.conv.Entry(v)
	case *domain.EntryView:
		wire = s.conv.Entry(*v)
	case domain.BlockView:
		wire = s.conv.Block(v)
	case *domain.BlockView:
		wire = s.conv.Block(*v)
	default:
		return nil, domain.Errorf(domain.CodeInternal, "unsupported current representation")
	}
	return json.Marshal(wire)
}

func (s *Server) clockResponse(v service.ClockView) api.Clock {
	return api.Clock{Now: s.conv.Time(v.Now), Today: v.Today, Timezone: v.Zone, WeekStart: strings.ToLower(v.WeekStart.String()), DueSoonDays: v.DueSoonDays, ServerVersion: buildinfo.Version, Generation: string(v.Generation)}
}

func (s *Server) todayResponse(v service.TodayView) api.Today {
	out := api.Today{
		Clock: s.clockResponse(v.Clock), Blocks: convertSlice(v.Blocks, s.conv.Block),
		TriageCount: v.TriageCount, TriageTop: convertSlice(v.TriageTop, s.conv.Task),
		Overdue: convertSlice(v.Overdue, s.conv.Task), DueSoon: convertSlice(v.DueSoon, s.conv.Task),
		Entries: convertSlice(v.Entries, s.conv.Entry), Interruptions: convertSlice(v.Interruptions, s.conv.Interruption),
		AIChanges: v.AIChanges, Backup: api.BackupHealth{LastSuccessAt: s.conv.OptTime(v.Backup.LastSuccessAt), LastError: v.Backup.LastError, Healthy: v.Backup.Healthy},
		Banners: convertSlice(v.Banners, func(v service.Banner) api.Banner { return api.Banner{Kind: v.Kind, Message: v.Message} }),
	}
	if v.Focus != nil {
		out.Focus = new(s.conv.Task(*v.Focus))
	}
	if v.CurrentBlock != nil {
		out.CurrentBlock = new(s.conv.Block(*v.CurrentBlock))
	}
	return out
}

func statusResponse(v service.StatusView) api.Status {
	out := api.Status{Triage: v.Triage, Due: v.Due, AI: v.AI, BackupWarn: v.BackupWarn, PurgeWarning: v.PurgeWarning}
	if v.Focus != nil {
		out.Focus = &api.StatusTask{ID: int64(v.Focus.ID), Title: v.Focus.Title}
	}
	if v.CurrentBlock != nil {
		out.CurrentBlock = &api.StatusBlock{ID: int64(v.CurrentBlock.ID), TaskID: int64(v.CurrentBlock.TaskID), Title: v.CurrentBlock.Title, End: v.CurrentBlock.End}
	}
	return out
}

func (s *Server) dayResponse(v service.DayView) api.Day {
	return api.Day{Date: v.Date, Blocks: convertSlice(v.Blocks, s.conv.Block), Entries: convertSlice(v.Entries, s.conv.Entry), Unlinked: convertSlice(v.Unlinked, s.conv.Entry), Interruptions: convertSlice(v.Interruptions, s.conv.Interruption)}
}

func (s *Server) reportResponse(v service.Report) api.Report {
	out := api.Report{
		From: v.From, To: v.To, Tag: optionalString(v.Tag), Project: optionalString(v.Project),
		Days: convertSlice(v.Days, func(v service.ReportDay) api.ReportDay {
			return api.ReportDay{Date: v.Date, Lines: convertSlice(v.Lines, func(v service.ReportLine) api.ReportLine {
				return api.ReportLine{EntryID: int64(v.EntryID), Kind: string(v.Kind), Title: v.Title, Notes: v.Notes, TaskID: optionalID(v.TaskID), Time: optionalValue(v.Time), Reopened: v.Reopened}
			})}
		}),
		Tasks: make(map[string]api.ReportTask, len(v.Tasks)), Upcoming: convertSlice(v.Upcoming, s.conv.Task),
	}
	for id, task := range v.Tasks {
		out.Tasks[strconv.FormatInt(int64(id), 10)] = api.ReportTask{Title: task.Title, Status: string(task.Status), Deleted: task.Deleted}
	}
	if v.Todo != nil {
		out.Todo = &api.ReportTodo{Triage: convertSlice(v.Todo.Triage, s.conv.Task), Backlog: convertSlice(v.Todo.Backlog, s.conv.Task)}
	}
	return out
}

func searchHitResponse(v service.SearchHit) api.SearchHit {
	return api.SearchHit{Kind: v.Kind, ID: v.ID, TaskID: optionalID(v.TaskID), Title: v.Title, Snippet: v.Snippet, Status: string(v.Status), WorkDate: optionalValue(v.WorkDate)}
}

func (s *Server) opSummaryResponse(v service.OpSummary) api.OpSummary {
	return api.OpSummary{
		Seq: int64(v.Seq), OpID: v.OpID.String(), Kind: string(v.Kind), Actor: v.Actor, Channel: string(v.Channel),
		At: s.conv.Time(v.At), State: string(v.State), Revertible: v.Revertible, Via: optionalString(v.Via),
		TaskIDs: convertSlice(v.TaskIDs, func(id domain.TaskID) int64 { return int64(id) }),
		Effects: convertSlice(v.Effects, effectResponse),
	}
}

func (s *Server) eventResponse(v service.EventView) api.EventView {
	return api.EventView{ID: int64(v.ID), Op: s.opSummaryResponse(v.Op), Entity: string(v.Entity), EntityID: v.EntityID, Action: string(v.Action), Before: v.Before, After: v.After}
}

func effectResponse(v service.Effect) api.Effect {
	return api.Effect{Kind: string(v.Kind), TaskID: int64(v.TaskID), BlockID: int64(v.BlockID), EntryID: int64(v.EntryID),
		Reason: string(v.Reason), Via: v.Via, From: v.From, To: v.To}
}

func convertSlice[T, U any](values []T, convert func(T) U) []U {
	out := make([]U, len(values))
	for i, v := range values {
		out[i] = convert(v)
	}
	return out
}

func pageResponse[T, U any](v service.Page[T], convert func(T) U) api.Page[U] {
	return api.Page[U]{Items: convertSlice(v.Items, convert), NextCursor: optionalString(v.NextCursor)}
}

func optionalString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func optionalValue[T any](v sql.Null[T]) *T {
	if !v.Valid {
		return nil
	}
	return &v.V
}

func optionalID[T ~int64](v sql.Null[T]) *int64 {
	if !v.Valid {
		return nil
	}
	return new(int64(v.V))
}
