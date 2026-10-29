package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/config"
	"taskronin/internal/datespec"
)

type env struct {
	ctx                context.Context
	argv               []string
	in                 io.Reader
	out, err           io.Writer
	opOutputErr        error
	sys                sysDeps
	jsonOut            bool
	socket, tokenFile  string
	color              string
	colorSet           bool
	paths              config.Paths
	ccfg               *config.Client
	apiClient          *client.Client
	serverClock        *api.Clock
	clockLoaded        bool
	clockErr           error
	executing, mutates bool
	draftPath          string
}

// TUIRequest is the shared launch contract implemented by Plan C.
type TUIRequest struct {
	Mode      string // "tabs" | "triage" | "plan" | "recap"
	Tab       string // "" | today | triage | backlog | upcoming | log
	RecapDate string // raw datespec; "" = today, resolved against GET /clock in Past mode
}

// launchTUI is assigned by Plan C's cmd_tui.go init().
var launchTUI func(cmd *cobra.Command, req TUIRequest) error

var commandFactories []func(*env) *cobra.Command

// register is called only from command files' init functions. Each invocation
// builds fresh commands so flags and dependencies never leak between runs.
func register(f func(e *env) *cobra.Command) { commandFactories = append(commandFactories, f) }

type envKey struct{}

func envFrom(cmd *cobra.Command) *env {
	e, _ := cmd.Context().Value(envKey{}).(*env)
	return e
}

func newRoot(e *env) *cobra.Command {
	root := &cobra.Command{
		Use: "tkr", Short: "Taskronin task manager", SilenceUsage: true, SilenceErrors: true,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			e.executing = true
			e.colorSet = cmd.Flags().Changed("color")
			for c := cmd; c != nil; c = c.Parent() {
				if c.Annotations["mutates"] == "true" {
					e.mutates = true
				}
			}
			switch e.color {
			case "auto", "always", "never":
			default:
				return usagef("--color must be auto, always or never")
			}
			return e.ctx.Err()
		},
	}
	root.SetIn(e.in)
	root.SetOut(e.out)
	root.SetErr(e.err)
	// AIDEV-NOTE: put the invocation in the context passed to ExecuteContextC;
	// assigning only root.Context here would be overwritten by Cobra's execute.
	e.ctx = context.WithValue(e.ctx, envKey{}, e)
	root.SetContext(e.ctx)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usagef("%s", err) })
	f := root.PersistentFlags()
	f.BoolVar(&e.jsonOut, "json", false, "Print API JSON or problem JSON")
	f.StringVar(&e.socket, "socket", "", "Override the server socket path")
	f.StringVar(&e.tokenFile, "token-file", "", "Override the token file path")
	f.StringVar(&e.color, "color", "auto", "Color output: auto, always, never")
	for _, factory := range commandFactories {
		root.AddCommand(factory(e))
	}
	return root
}

func (e *env) clientConfig() (*config.Client, error) {
	if e.ccfg != nil {
		return e.ccfg, nil
	}
	overrides := config.ClientOverrides{Socket: e.socket, TokenFile: e.tokenFile}
	if e.colorSet {
		overrides.Color = e.color
	}
	// AIDEV-NOTE: validate only after flags replace defaults/config values;
	// otherwise --token-file cannot recover from a rejected configured token.
	cfg, err := config.LoadClientWithOverrides(e.paths.ClientConfig, e.paths, overrides)
	if err != nil {
		return nil, tokenConfigError(err)
	}
	e.ccfg = cfg
	return cfg, nil
}

func (e *env) client() (*client.Client, error) {
	if e.apiClient != nil {
		return e.apiClient, nil
	}
	cfg, err := e.clientConfig()
	if err != nil {
		return nil, err
	}
	e.apiClient, err = client.NewFromConfig(cfg, "cli", &client.PendingStore{Dir: e.paths.PendingDir})
	return e.apiClient, err
}

func (e *env) clock() (*api.Clock, error) {
	if !e.clockLoaded {
		e.clockLoaded = true
		var c *client.Client
		c, e.clockErr = e.client()
		if e.clockErr == nil {
			e.serverClock, e.clockErr = c.Clock(e.ctx)
		}
	}
	return e.serverClock, e.clockErr
}

// The parse mode belongs to datespec.Parse; both modes use the same server date.
func (e *env) ref(_ datespec.Mode) (datespec.Ref, error) {
	clock, err := e.clock()
	if err != nil {
		return datespec.Ref{}, err
	}
	for day := time.Sunday; day <= time.Saturday; day++ {
		if strings.ToLower(day.String()) == clock.WeekStart {
			return datespec.Ref{Today: clock.Today, WeekStart: day}, nil
		}
	}
	return datespec.Ref{}, fmt.Errorf("server clock has invalid week_start %q", clock.WeekStart)
}
