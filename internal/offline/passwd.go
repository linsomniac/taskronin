package offline

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/linsomniac/taskronin/internal/auth"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/fsutil"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/textsafe"
)

// PasswdReset resets the owner password with the server stopped (A§12.8): it
// replaces the password, revokes every token and web session, and writes a
// fresh owner token to the owner token file.
//
// AIDEV-NOTE: SECURITY. The new password is read only from the terminal (twice)
// or one stdin line, never from argv or the environment, and is never logged or
// persisted: only its hash reaches the DB. The owner token secret exists only in
// memory until the owner.token file is written. A crash after the commit but
// before the file write leaves a file holding a revoked token; the reset is
// re-runnable (it needs no current password) and the CLI says so on a write
// failure (A§12.8, crash point "passwd:committed").
func PasswdReset(ctx context.Context, e *Env, passwordStdin bool) (err error) {
	release, err := e.begin(ctx, refuseJournals)
	if err != nil {
		return err
	}
	defer release()
	password, err := passwdReadNew(ctx, e, passwordStdin)
	if err != nil {
		return err
	}
	if err := auth.ValidateNewPassword(password); err != nil {
		return domain.Errorf(domain.CodeValidation, "%s", err)
	}
	secret, err := auth.NewSecret()
	if err != nil {
		return offlineError(err)
	}
	st, svc, err := e.openLive(ctx)
	if err != nil {
		return err
	}
	defer func() {
		// Keep err's own type: the CLI relies on the re-run hint and domain codes.
		if cerr := st.Close(); cerr != nil {
			err = errors.Join(err, offlineError(cerr))
		}
	}()
	id, err := svc.ResetPasswordOffline(ctx, service.ResetPasswordIn{NewPassword: password, OwnerSecret: secret})
	if err != nil {
		return err
	}
	if err := crashPoint(ctx, "passwd:committed"); err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(e.Paths.OwnerToken, []byte(auth.FormatToken(id, secret)+"\n"), 0o600); err != nil {
		return fmt.Errorf("the password was changed but %s could not be written: %s — re-run `tkr passwd --reset`",
			textsafe.SanitizeLine(e.Paths.OwnerToken), textsafe.SanitizeLine(err.Error()))
	}
	if err := checkpoint(ctx, st); err != nil {
		return err
	}
	_, err = fmt.Fprintln(e.Out, "password reset; all tokens and web sessions revoked; new owner token written")
	return offlineError(err)
}

// passwdReadNew reads the replacement password: one stdin line with
// --password-stdin, otherwise twice from the terminal.
func passwdReadNew(ctx context.Context, e *Env, passwordStdin bool) (string, error) {
	if passwordStdin {
		var reader *bufio.Reader
		switch in := e.In.(type) {
		case *bufio.Reader:
			reader = in
		case nil:
			reader = bufio.NewReader(strings.NewReader(""))
		default:
			reader = bufio.NewReader(in)
		}
		password, err := initReadLine(reader)
		if err == nil {
			err = ctx.Err()
		}
		return password, offlineError(err)
	}
	if !e.TTY {
		return "", domain.Errorf(domain.CodePasswordRequired, "enter the password on a terminal or use --password-stdin")
	}
	if e.ReadPassword == nil {
		return "", domain.Errorf(domain.CodeInternal, "password reader is not configured")
	}
	password, err := e.ReadPassword("New password: ")
	if err != nil {
		return "", offlineError(err)
	}
	repeat, err := e.ReadPassword("Repeat password: ")
	if err != nil {
		return "", offlineError(err)
	}
	if password != repeat {
		return "", domain.Errorf(domain.CodeValidation, "passwords do not match")
	}
	return password, offlineError(ctx.Err())
}
