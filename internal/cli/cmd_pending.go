package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"taskronin/internal/client"
	"taskronin/internal/domain"
	"taskronin/internal/textsafe"
)

func init() { register(pendingCommand) }

func (e *env) pendingStore() *client.PendingStore {
	return &client.PendingStore{Dir: e.paths.PendingDir}
}

func pendingCommand(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use: "pending", Short: "List saved requests whose outcome was unknown", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			store := e.pendingStore()
			list, err := store.List()
			if err != nil {
				return err
			}
			if e.jsonOut {
				if list == nil {
					list = []client.PendingRequest{}
				}
				return json.NewEncoder(e.out).Encode(list)
			}
			if len(list) == 0 {
				_, err := fmt.Fprintln(e.out, "no pending requests")
				return err
			}
			var out strings.Builder
			for _, p := range list {
				line := fmt.Sprintf("%s  %s  %s %s", p.Key, ageText(store.Age(p)), p.Method, p.Path)
				if p.LastError != "" {
					line += "  last error: " + p.LastError
				}
				fmt.Fprintln(&out, textsafe.SanitizeLine(line))
			}
			_, err = fmt.Fprint(e.out, out.String())
			return err
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use: "retry [KEY]", Short: "Resend saved requests with their original idempotency keys", Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error { return e.pendingRetry(args) },
	}, &cobra.Command{
		Use: "discard KEY", Short: "Delete a saved request without resending it", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := e.pendingStore().Discard(args[0]); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return domain.Errorf(domain.CodeNotFound, "no pending request %q", args[0])
				}
				return err
			}
			_, err := fmt.Fprintf(e.out, "discarded pending request %s\n", textsafe.SanitizeLine(args[0]))
			return err
		},
	})
	return cmd
}

func ageText(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// pendingRetry resends each selected request (oldest first) with its original key.
// A definitive problem keeps the file and records it; a transport failure stops
// the run because the server is unreachable. The first error is returned.
func (e *env) pendingRetry(args []string) error {
	e.resending = true
	store := e.pendingStore()
	list, err := store.List()
	if err != nil {
		return err
	}
	if len(args) == 1 {
		var picked []client.PendingRequest
		for _, p := range list {
			if p.Key == args[0] {
				picked = append(picked, p)
			}
		}
		if len(picked) == 0 {
			return domain.Errorf(domain.CodeNotFound, "no pending request %q", args[0])
		}
		list = picked
	}
	if len(list) == 0 {
		_, err := fmt.Fprintln(e.out, "no pending requests")
		return err
	}
	c, err := e.client()
	if err != nil {
		return err
	}
	var first error
	for _, p := range list {
		err := e.retryOne(c, store, p)
		if err == nil {
			continue
		}
		if first == nil {
			first = err
		} else {
			e.errorLine("tkr: pending request %s: %s", p.Key, problemFor(err).Detail)
		}
		if errors.Is(err, client.ErrNotSent) || errors.Is(err, client.ErrOutcomeUnknown) {
			break
		}
	}
	return first
}

func (e *env) retryOne(c *client.Client, store *client.PendingStore, p client.PendingRequest) error {
	res, err := c.Resend(e.ctx, p)
	switch {
	case errors.Is(err, client.ErrPendingExpired):
		return fmt.Errorf("%w — check the outcome with `tkr show` / `tkr entries`, then run `tkr pending discard %s`", err, p.Key)
	case errors.Is(err, client.ErrGenerationMismatch), errors.Is(err, client.ErrActorMismatch):
		return fmt.Errorf("%w — it cannot be resent; run `tkr pending discard %s`", err, p.Key)
	case err != nil:
		var problem *client.ProblemError
		if errors.As(err, &problem) {
			// AIDEV-NOTE: a definitive refusal keeps the saved body (M1) and
			// records why; only success or explicit discard removes the file.
			if markErr := store.MarkFailed(p.Key, problem.Problem.Detail); markErr != nil {
				return errors.Join(err, markErr)
			}
		}
		return err
	}
	if err := store.Discard(p.Key); err != nil {
		return err
	}
	switch {
	case res.Token != nil:
		_, err = fmt.Fprintf(e.out, "revoked token %s (id %d)\n", textsafe.SanitizeLine(res.Token.Name), res.Token.ID)
		return err
	case res.Op != nil && res.Op.OpID != nil:
		return e.outputOp(res.Op, fmt.Sprintf("resent pending request %s (op %.8s)", p.Key, *res.Op.OpID))
	default:
		_, err = fmt.Fprintf(e.out, "resent pending request %s\n", textsafe.SanitizeLine(p.Key))
		return err
	}
}
