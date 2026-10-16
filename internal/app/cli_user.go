package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/bropines/rdpanel/internal/auth"
	"github.com/bropines/rdpanel/internal/config"
	"github.com/bropines/rdpanel/internal/db"
	"github.com/bropines/rdpanel/internal/store"
)

// minPasswordChars is enforced for passwords set from the CLI.
const minPasswordChars = 8

// cliActor is the admin_audit actor name for command-line actions.
const cliActor = "cli"

func cmdUserCreate(ctx context.Context, args []string, env config.LookupFunc, stdio Stdio) error {
	fs := newFlagSet("user create", stdio)
	admin := fs.Bool("admin", false, "grant the admin role")
	fromStdin := fs.Bool("password-stdin", false, "read the password from the first line of stdin")
	email := fs.String("email", "", "email address")
	displayName := fs.String("display-name", "", "display name")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return usageErr(err)
	}
	username, err := singleUsername(pos)
	if err != nil {
		return err
	}

	st, closeDB, err := openStoreForCLI(ctx, env, stdio)
	if err != nil {
		return err
	}
	defer closeDB()

	if _, err := st.Users.GetByUsername(ctx, username); err == nil {
		return fmt.Errorf("user %q already exists (use `user reset-password`)", username)
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}

	password, err := readPassword(stdio, *fromStdin)
	if err != nil {
		return err
	}
	hash, algo, err := auth.NewHasher().Hash(password)
	if err != nil {
		return err
	}

	role := store.RoleUser
	if *admin {
		role = store.RoleAdmin
	}
	u := &store.User{Username: username, Email: strings.TrimSpace(*email), DisplayName: *displayName,
		PasswordHash: hash, PasswordAlgo: string(algo), Role: role}
	err = st.Tx(ctx, func(tx *store.Store) error {
		if err := tx.Users.Create(ctx, u); err != nil {
			return err
		}
		return tx.AdminAudit.Record(ctx, &store.AuditEntry{ActorName: cliActor, Action: "user.create",
			TargetType: "user", TargetID: strconv.FormatInt(u.ID, 10),
			Details: map[string]any{"username": u.Username, "role": string(u.Role)}})
	})
	if errors.Is(err, store.ErrConflict) {
		return fmt.Errorf("user %q already exists", username)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stdio.Out, "created user %q (id %d, role %s)\n", u.Username, u.ID, u.Role)
	return nil
}

func cmdUserResetPassword(ctx context.Context, args []string, env config.LookupFunc, stdio Stdio) error {
	fs := newFlagSet("user reset-password", stdio)
	fromStdin := fs.Bool("password-stdin", false, "read the password from the first line of stdin")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return usageErr(err)
	}
	username, err := singleUsername(pos)
	if err != nil {
		return err
	}

	st, closeDB, err := openStoreForCLI(ctx, env, stdio)
	if err != nil {
		return err
	}
	defer closeDB()

	u, err := st.Users.GetByUsername(ctx, username)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("user %q not found", username)
	} else if err != nil {
		return err
	}
	password, err := readPassword(stdio, *fromStdin)
	if err != nil {
		return err
	}
	hash, algo, err := auth.NewHasher().Hash(password)
	if err != nil {
		return err
	}
	var revoked int64
	err = st.Tx(ctx, func(tx *store.Store) error {
		if err := tx.Users.SetPassword(ctx, u.ID, hash, string(algo)); err != nil {
			return err
		}
		// A password reset ends every session and client login (legacy did the same).
		if revoked, err = tx.AuthTokens.RevokeForUser(ctx, u.ID, "", "password reset"); err != nil {
			return err
		}
		return tx.AdminAudit.Record(ctx, &store.AuditEntry{ActorName: cliActor, Action: "user.reset_password",
			TargetType: "user", TargetID: strconv.FormatInt(u.ID, 10),
			Details: map[string]any{"username": u.Username, "revoked_tokens": revoked}})
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdio.Out, "password updated for %q; revoked %d token(s)\n", u.Username, revoked)
	return nil
}

// openStoreForCLI opens and migrates the database without loading keys or
// starting the HTTP stack.
func openStoreForCLI(ctx context.Context, env config.LookupFunc, stdio Stdio) (*store.Store, func(), error) {
	cfg, err := config.Load(env)
	if err != nil {
		return nil, nil, err
	}
	log := NewLogger(cfg.Log, stdio.Err)
	var d *db.DB
	if d, err = openDB(ctx, cfg, log); err != nil {
		return nil, nil, err
	}
	return store.New(d), func() { _ = d.Close() }, nil
}

// readPassword prompts on a terminal (twice, for confirmation) or reads the
// first line of stdin.
func readPassword(stdio Stdio, fromStdin bool) (string, error) {
	var pw string
	if f, ok := stdio.In.(*os.File); ok && !fromStdin && term.IsTerminal(int(f.Fd())) {
		first, err := promptHidden(f, stdio.Err, "Password: ")
		if err != nil {
			return "", err
		}
		second, err := promptHidden(f, stdio.Err, "Repeat password: ")
		if err != nil {
			return "", err
		}
		if first != second {
			return "", errors.New("passwords do not match")
		}
		pw = first
	} else {
		line, err := bufio.NewReader(stdio.In).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read password: %w", err)
		}
		pw = strings.TrimRight(line, "\r\n")
	}
	if utf8.RuneCountInString(pw) < minPasswordChars {
		return "", fmt.Errorf("password must be at least %d characters", minPasswordChars)
	}
	if len(pw) > auth.MaxPasswordBytes {
		return "", fmt.Errorf("password must be at most %d bytes", auth.MaxPasswordBytes)
	}
	return pw, nil
}

func promptHidden(f *os.File, w io.Writer, prompt string) (string, error) {
	fmt.Fprint(w, prompt)
	b, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(w)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(b), nil
}
