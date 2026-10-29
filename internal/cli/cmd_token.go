package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"taskronin/internal/api"
	"taskronin/internal/civil"
	"taskronin/internal/client"
	"taskronin/internal/domain"
	"taskronin/internal/fsutil"
	"taskronin/internal/textsafe"
)

func init() { register(tokenCommand) }

const mcpScopes, mcpRate = "read,capture,write", 60

// writeFileDefault is the NoOptDefVal marking `--write-file` without a path.
const writeFileDefault = "\x00default"

var durationExpiry = regexp.MustCompile(`^([0-9]{1,6})([hd])$`)

func tokenCommand(e *env) *cobra.Command {
	cmd := &cobra.Command{Use: "token", Short: "Manage API tokens", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	cmd.AddCommand(tokenCreateCommand(e), tokenListCommand(e), tokenRevokeCommand(e))
	return cmd
}

func tokenCreateCommand(e *env) *cobra.Command {
	var scopes, expires, writeFile string
	var mcp bool
	var rate int
	cmd := &cobra.Command{
		Use: "create NAME", Short: "Create a token and print its secret once", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := textsafe.ValidateName(name, 64); err != nil {
				return usagef("token name %s", err)
			}
			if mcp == (scopes != "") {
				return usagef("choose exactly one of --scopes LIST or --mcp")
			}
			if mcp {
				scopes = mcpScopes
				if !cmd.Flags().Changed("rate") {
					rate = mcpRate
				}
			}
			if rate < 0 {
				return usagef("--rate must not be negative")
			}
			if _, err := domain.ParseScopes(scopes); err != nil {
				return usagef("--scopes: %s", err)
			}
			if expires == "" {
				answer, err := e.promptExpiry()
				if err != nil {
					return err
				}
				expires = answer
			}
			path := ""
			if cmd.Flags().Changed("write-file") {
				path = writeFile
				if path == writeFileDefault {
					path = e.paths.TokenFile(name)
				}
				// A refused overwrite must not first mint a token whose secret is lost.
				if _, err := os.Lstat(path); err == nil {
					return domain.Errorf(domain.CodeConflict, "token file %s already exists; it was not changed", path)
				} else if !errors.Is(err, fs.ErrNotExist) {
					return err
				}
			}
			c, err := e.client()
			if err != nil {
				return err
			}
			e.mutates = true
			expiresAt, err := e.parseExpiry(expires)
			if err != nil {
				return err
			}
			created, err := c.CreateToken(e.ctx, api.CreateTokenRequest{Name: name, Scopes: scopes, ExpiresAt: expiresAt, MutationsPerHour: rate})
			if err != nil {
				return err
			}
			if path != "" {
				if err := writeTokenFile(e, path, created.Secret); err != nil {
					return fmt.Errorf("token %q (id %d) was created but its secret could not be saved: %w; revoke it with `tkr token revoke %d` and retry", name, created.Token.ID, err, created.Token.ID)
				}
				if e.jsonOut {
					return json.NewEncoder(e.out).Encode(struct {
						Token api.TokenInfo `json:"token"`
						File  string        `json:"file"`
					}{created.Token, path})
				}
				_, err := fmt.Fprintf(e.out, "created token %s (id %d); secret written to %s\n", textsafe.SanitizeLine(name), created.Token.ID, textsafe.SanitizeLine(path))
				return err
			}
			if e.jsonOut {
				return json.NewEncoder(e.out).Encode(created) //nolint:gosec // G117: --json prints the one-time secret by design; --write-file keeps it out of output.
			}
			_, err = fmt.Fprintf(e.out, "created token %s (id %d)\nsecret: %s\n", textsafe.SanitizeLine(name), created.Token.ID, created.Secret)
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&scopes, "scopes", "", "Comma-separated scopes")
	f.BoolVar(&mcp, "mcp", false, "Use the MCP scopes ("+mcpScopes+") and a rate of 60/hour")
	f.StringVar(&expires, "expires", "", "Expiry: DURATION (e.g. 12h, 30d), DATE, or never")
	f.IntVar(&rate, "rate", 0, "Mutations per hour (0 = unlimited)")
	f.StringVar(&writeFile, "write-file", "", "Write the secret to a 0600 file (default: the token directory) instead of printing it; give a path as --write-file=PATH")
	f.Lookup("write-file").NoOptDefVal = writeFileDefault
	return cmd
}

func (e *env) promptExpiry() (string, error) {
	if !e.sys.StdinTTY {
		return "", usagef("--expires is required when not on a terminal (DURATION, DATE or never)")
	}
	if _, err := fmt.Fprint(e.err, "Expires (e.g. 30d, 2026-12-31, never): "); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(e.in).ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", err
	}
	if line = strings.TrimSpace(line); line == "" {
		return "", usagef("an expiry is required (DURATION, DATE or never)")
	}
	return line, nil
}

// parseExpiry resolves DUR against the server's now and DATE to 00:00 in the
// server's zone, so a token never expires at a surprising local time.
func (e *env) parseExpiry(s string) (api.Opt[api.Time], error) {
	if s == "never" {
		return api.Opt[api.Time]{Set: true, Null: true}, nil
	}
	clock, err := e.clock()
	if err != nil {
		return api.Opt[api.Time]{}, err
	}
	var at time.Time
	if m := durationExpiry.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		unit := time.Hour
		if m[2] == "d" {
			unit = 24 * time.Hour
		}
		at = clock.Now.Add(time.Duration(n) * unit)
	} else if date, dateErr := civil.ParseDate(s); dateErr == nil {
		zone, err := civil.LoadZone(clock.Timezone)
		if err != nil {
			return api.Opt[api.Time]{}, err
		}
		at = zone.StartOfDay(date)
	} else {
		return api.Opt[api.Time]{}, usagef("--expires must be a duration like 12h or 30d, a date like 2026-12-31, or never")
	}
	if !at.After(clock.Now.Time) {
		return api.Opt[api.Time]{}, usagef("--expires must be in the future")
	}
	return api.Opt[api.Time]{Set: true, V: api.Time{Time: at}}, nil
}

func writeTokenFile(e *env, path, secret string) error {
	if filepath.Dir(path) == e.paths.TokensDir {
		if err := fsutil.EnsureDir0700(e.paths.TokensDir); err != nil {
			return err
		}
	}
	// Re-check: another process may have created it while the server call ran.
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	return fsutil.WriteFileAtomic(path, []byte(secret+"\n"), 0o600)
}

func tokenListCommand(e *env) *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List tokens", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := e.client()
			if err != nil {
				return err
			}
			tokens, err := c.Tokens(e.ctx)
			if err != nil {
				return err
			}
			if e.jsonOut {
				return json.NewEncoder(e.out).Encode(tokens)
			}
			var out strings.Builder
			for _, t := range tokens {
				line := fmt.Sprintf("%d %s [%s] rate %s expires %s last used %s", t.ID, t.Name, t.Scopes, rateText(t.MutationsPerHour), timeText(t.ExpiresAt, "never"), timeText(t.LastUsedAt, "never"))
				if t.Owner {
					line += " (owner)"
				}
				if t.RevokedAt != nil {
					line += " (revoked " + timeText(t.RevokedAt, "") + ")"
				}
				fmt.Fprintln(&out, textsafe.SanitizeLine(line))
			}
			_, err = fmt.Fprint(e.out, out.String())
			return err
		}}
}

func rateText(perHour int) string {
	if perHour == 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d/h", perHour)
}

func timeText(t *api.Time, none string) string {
	if t == nil {
		return none
	}
	return t.Format("2006-01-02 15:04Z07:00")
}

func tokenRevokeCommand(e *env) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use: "revoke ID|NAME", Short: "Revoke a token", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if !yes && !e.sys.StdinTTY {
				return usagef("confirmation requires a terminal or -y")
			}
			c, err := e.client()
			if err != nil {
				return err
			}
			tokens, err := c.Tokens(e.ctx)
			if err != nil {
				return err
			}
			target, err := findToken(tokens, args[0])
			if err != nil {
				return err
			}
			if !yes {
				ok, err := e.offlineEnv().Confirm(fmt.Sprintf("Revoke token %s (id %d)?", target.Name, target.ID), false)
				if err != nil {
					return err
				}
				if !ok {
					return domain.Errorf(domain.CodeConflict, "token not revoked")
				}
			}
			e.mutates = true
			revoked, err := c.RevokeToken(e.ctx, client.Mut{}, target.ID)
			if err != nil {
				return err
			}
			if e.jsonOut {
				return json.NewEncoder(e.out).Encode(revoked)
			}
			_, err = fmt.Fprintf(e.out, "revoked token %s (id %d)\n", textsafe.SanitizeLine(revoked.Name), revoked.ID)
			return err
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Do not ask for confirmation")
	return cmd
}

// findToken matches a numeric ID first, then a name, among live tokens.
func findToken(tokens []api.TokenInfo, ref string) (api.TokenInfo, error) {
	id, idErr := strconv.ParseInt(ref, 10, 64)
	for _, t := range tokens {
		if t.RevokedAt == nil && ((idErr == nil && t.ID == id) || t.Name == ref) {
			return t, nil
		}
	}
	return api.TokenInfo{}, domain.Errorf(domain.CodeNotFound, "no active token %q", ref)
}
