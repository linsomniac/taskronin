package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/client"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/testutil"
)

const eofRequests = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"eof-test","version":"1"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"add_task","arguments":{"title":"captured before EOF"}}}
`

func TestRunStdinEOFDrainsRequests(t *testing.T) {
	srv := testutil.StartServer(t)
	ai := srv.TokenClient(t, "ai", domain.MCPDefaultScopes, 60, "mcp")
	in, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close() })
	if _, err := io.WriteString(in, eofRequests); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	out, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })
	stdin, stdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = in, out
	defer func() { os.Stdin, os.Stdout = stdin, stdout }()
	if err := Run(t.Context(), Options{Client: ai, Logger: quietLogger()}); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	responses := map[int]json.RawMessage{}
	dec := json.NewDecoder(out)
	for {
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := dec.Decode(&response); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if len(response.Error) > 0 {
			t.Errorf("response %d error: %s", response.ID, response.Error)
		}
		if _, exists := responses[response.ID]; exists {
			t.Fatalf("duplicate response %d", response.ID)
		}
		responses[response.ID] = response.Result
	}
	for _, id := range []int{1, 2, 3} {
		if len(responses[id]) == 0 {
			t.Errorf("missing successful response %d: %v", id, responses)
		}
	}
	if t.Failed() {
		return
	}
	var listed mcp.ListToolsResult
	if err := json.Unmarshal(responses[2], &listed); err != nil {
		t.Fatal(err)
	}
	if toolByName(listed.Tools, "add_task") == nil {
		t.Fatal("tools/list omitted add_task")
	}
	var added struct {
		IsError           bool           `json:"isError"`
		StructuredContent api.OpResponse `json:"structuredContent"`
	}
	if err := json.Unmarshal(responses[3], &added); err != nil {
		t.Fatal(err)
	}
	if added.IsError || added.StructuredContent.Task == nil {
		t.Fatalf("add_task failed: %s", responses[3])
	}
	page, err := srv.Client.ListTasks(t.Context(), client.TaskQuery{View: "triage"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Title != "captured before EOF" || page.Items[0].ID != added.StructuredContent.Task.ID {
		t.Fatalf("tasks after EOF = %+v; want exactly the acknowledged task", page.Items)
	}
}

func TestStdinEOFDrainBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, &mcp.ServerOptions{Logger: quietLogger()})
		started := make(chan context.Context, 1)
		release := make(chan struct{})
		defer close(release)
		s.AddTool(&mcp.Tool{Name: "add_task", InputSchema: map[string]any{"type": "object"}},
			func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				started <- ctx
				<-release // Deliberately ignore cancellation to exercise the hard bound.
				return &mcp.CallToolResult{}, nil
			})
		var out bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- runDraining(t.Context(), s, io.NopCloser(strings.NewReader(eofRequests)), &out, 30*time.Second)
		}()
		requestCtx := <-started
		time.Sleep(29 * time.Second)
		if err := requestCtx.Err(); err != nil {
			t.Fatalf("stdin EOF cancelled handler before bound: %v", err)
		}
		select {
		case err := <-done:
			t.Fatalf("returned before drain bound: %v", err)
		default:
		}
		time.Sleep(time.Second)
		synctest.Wait()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("did not return at 30-second drain bound")
		}
		if requestCtx.Err() == nil {
			t.Error("forced close did not cancel the stalled request")
		}
	})
}

func TestStdinEOFEmptyAndProtocolErrors(t *testing.T) {
	initialize, _, _ := strings.Cut(eofRequests, "\n")
	for _, tc := range []struct {
		name, input string
		errorCode   int
	}{
		{"empty", "", 0},
		{"unknown method", initialize + "\n" + `{"jsonrpc":"2.0","id":2,"method":"unknown"}` + "\n", -32601},
		{"invalid params", initialize + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":[]}` + "\n", -32602},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, &mcp.ServerOptions{Logger: quietLogger()})
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				var out bytes.Buffer
				if err := runDraining(ctx, s, io.NopCloser(strings.NewReader(tc.input)), &out, 30*time.Second); err != nil {
					t.Fatal(err)
				}
				if tc.errorCode == 0 {
					if out.Len() != 0 {
						t.Fatalf("empty input produced output: %s", &out)
					}
					return
				}
				dec := json.NewDecoder(&out)
				found := false
				for dec.More() {
					var response struct {
						ID    int `json:"id"`
						Error struct {
							Code int `json:"code"`
						} `json:"error"`
					}
					if err := dec.Decode(&response); err != nil {
						t.Fatal(err)
					}
					if response.ID == 2 {
						found = true
						if response.Error.Code != tc.errorCode {
							t.Errorf("error = %d; want %d", response.Error.Code, tc.errorCode)
						}
					}
				}
				if !found {
					t.Fatal("missing protocol error response at EOF")
				}
			})
		})
	}
}
