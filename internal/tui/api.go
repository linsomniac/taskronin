package tui

import (
	"context"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/client"
)

// API is the subset of client.Client used by the terminal user interface.
type API interface {
	Clock(ctx context.Context) (*api.Clock, error)
	Generation(ctx context.Context) (string, error)
	RefreshGeneration(ctx context.Context) (string, error)
	Today(ctx context.Context, ifNoneMatch string) (t *api.Today, etag string, notModified bool, err error)
	Day(ctx context.Context, d civil.Date) (*api.Day, error)
	Upcoming(ctx context.Context) (*api.Upcoming, error)
	Search(ctx context.Context, q client.SearchQuery) (*api.Page[api.SearchHit], error)
	ListTasks(ctx context.Context, q client.TaskQuery) (*api.Page[api.Task], error)
	GetTask(ctx context.Context, id int64) (*api.TaskDetail, error)
	TaskHistory(ctx context.Context, id int64, p client.PageQuery) (*api.Page[api.EventView], error)
	ListEntries(ctx context.Context, q client.EntryQuery) (*api.Page[api.Entry], error)
	ListBlocks(ctx context.Context, q client.BlockQuery) ([]api.Block, error)
	AIChanges(ctx context.Context, all bool, p client.PageQuery) (*api.AIChanges, error)
	AddTask(ctx context.Context, m client.Mut, r api.AddTaskRequest) (*api.OpResponse, error)
	UpdateTask(ctx context.Context, m client.Mut, id int64, r api.UpdateTaskRequest) (*api.OpResponse, error)
	PlaceTask(ctx context.Context, m client.Mut, id int64, r api.PlaceRequest) (*api.OpResponse, error)
	DeferTask(ctx context.Context, m client.Mut, id int64, r api.DeferRequest) (*api.OpResponse, error)
	BlockTask(ctx context.Context, m client.Mut, id int64, r api.BlockRequest) (*api.OpResponse, error)
	CompleteTask(ctx context.Context, m client.Mut, id int64, r api.CompleteRequest) (*api.OpResponse, error)
	DropTask(ctx context.Context, m client.Mut, id int64) (*api.OpResponse, error)
	AcceptNew(ctx context.Context, m client.Mut, r api.AcceptNewRequest) (*api.OpResponse, error)
	AddEntry(ctx context.Context, m client.Mut, r api.AddEntryRequest) (*api.OpResponse, error)
	UpdateEntry(ctx context.Context, m client.Mut, id int64, r api.UpdateEntryRequest) (*api.OpResponse, error)
	RelinkEntry(ctx context.Context, m client.Mut, id int64, r api.RelinkRequest) (*api.OpResponse, error)
	DeleteEntry(ctx context.Context, m client.Mut, id int64) (*api.OpResponse, error)
	Reblock(ctx context.Context, m client.Mut, blockID int64, r api.ReblockRequest) (*api.OpResponse, error)
	Unblock(ctx context.Context, m client.Mut, blockID int64) (*api.OpResponse, error)
	SetOutcome(ctx context.Context, m client.Mut, blockID int64, r api.OutcomeRequest) (*api.OpResponse, error)
	SetFocus(ctx context.Context, m client.Mut, r api.FocusRequest) (*api.OpResponse, error)
	Revert(ctx context.Context, m client.Mut, opID string) (*api.OpResponse, error)
	Undo(ctx context.Context, m client.Mut) (*api.OpResponse, error)
	Redo(ctx context.Context, m client.Mut) (*api.OpResponse, error)
	ReviewAIChanges(ctx context.Context, m client.Mut, r api.ReviewRequest) (*api.OpResponse, error)
}

var _ API = (*client.Client)(nil)
