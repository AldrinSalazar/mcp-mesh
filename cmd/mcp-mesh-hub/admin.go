package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/adminlog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/auth"
)

// adminCmd manages the admin users in the hub database. A running hub reads
// them from the database on every sign-in, so changes apply immediately.
func adminCmd(cfg *config) *cobra.Command {
	admin := &cobra.Command{
		Use:   "admin",
		Short: "Create, list and delete the admin users who sign in to the dashboard",
		Long: "Create, list and delete the admin users who sign in to the dashboard.\n\n" +
			"Admin users live in <data-dir>/hub.db. A running hub applies changes at once; changing a password or deleting a user signs them out.",
	}
	admin.AddCommand(adminCreateCmd(cfg), adminPasswdCmd(cfg), adminListCmd(cfg), adminDeleteCmd(cfg))
	return admin
}

func openUsers(cmd *cobra.Command, cfg *config) (context.Context, *auth.Users, func(), error) {
	ctx, db, closeDB, err := openDB(cmd, cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	users, err := auth.OpenUsers(ctx, db, nil)
	if err != nil {
		closeDB()
		return nil, nil, nil, err
	}
	return ctx, users, closeDB, nil
}

const passwordHelp = "read the password from one line of standard input instead of prompting (for scripts)"

// readPassword gets a new password: one line of stdin with --password-stdin,
// else a prompt asked twice without echo when stdin is a terminal.
func readPassword(cmd *cobra.Command, fromStdin bool) (string, error) {
	in := cmd.InOrStdin()
	if fromStdin {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && (!errors.Is(err, io.EOF) || line == "") {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	f, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", errors.New("no password: standard input is not a terminal, so pass --password-stdin and write the password to it")
	}
	prompt := func(label string) (string, error) {
		fmt.Fprint(cmd.ErrOrStderr(), label)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		return string(b), nil
	}
	first, err := prompt("Password: ")
	if err != nil {
		return "", err
	}
	second, err := prompt("Repeat password: ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errors.New("the passwords don't match")
	}
	return first, nil
}

func adminCreateCmd(cfg *config) *cobra.Command {
	var fromStdin bool
	cmd := &cobra.Command{
		Use:   "create <username>",
		Short: "Create an admin user",
		Long: fmt.Sprintf("Create an admin user. The password (at least %d characters) is prompted for twice, "+
			"or read from standard input with --password-stdin.", auth.MinPasswordLen),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			password, err := readPassword(cmd, fromStdin)
			if err != nil {
				return err
			}
			ctx, users, closeDB, err := openUsers(cmd, cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			user, err := users.Create(ctx, args[0], password)
			if err != nil {
				return err
			}
			cliRecord(cmd, cfg, adminlog.AdminCreate, args[0], "")
			adminURL, _, _ := cfg.URLs(port(cfg.AdminListen), port(cfg.NodeListen), port(cfg.ClientListen))
			fmt.Fprintf(cmd.OutOrStdout(), "Created admin user %q. Sign in at %s\n", user.Username, adminURL)
			return nil
		},
	}
	cmd.Flags().BoolVar(&fromStdin, "password-stdin", false, passwordHelp)
	return cmd
}

func adminPasswdCmd(cfg *config) *cobra.Command {
	var fromStdin bool
	cmd := &cobra.Command{
		Use:   "passwd <username>",
		Short: "Change an admin user's password and sign them out",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			password, err := readPassword(cmd, fromStdin)
			if err != nil {
				return err
			}
			ctx, users, closeDB, err := openUsers(cmd, cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			if err := users.SetPassword(ctx, args[0], password); err != nil {
				return err
			}
			cliRecord(cmd, cfg, adminlog.AdminPassword, args[0], "")
			fmt.Fprintf(cmd.OutOrStdout(), "Changed the password of %q. Their sessions were ended.\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&fromStdin, "password-stdin", false, passwordHelp)
	return cmd
}

type userJSON struct {
	Username  string     `json:"username"`
	Created   time.Time  `json:"created"`
	LastLogin *time.Time `json:"last_login"`
}

func adminListCmd(cfg *config) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List admin users",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, users, closeDB, err := openUsers(cmd, cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			list, err := users.List(ctx)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				rows := make([]userJSON, len(list))
				for i, u := range list {
					rows[i] = userJSON{Username: u.Username, Created: u.Created}
					if !u.LastLogin.IsZero() {
						rows[i].LastLogin = &list[i].LastLogin
					}
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "USERNAME\tCREATED\tLAST SIGN-IN")
			for _, u := range list {
				last := "never"
				if !u.LastLogin.IsZero() {
					last = u.LastLogin.Local().Format("2006-01-02 15:04")
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", u.Username, u.Created.Local().Format("2006-01-02 15:04"), last)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func adminDeleteCmd(cfg *config) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <username>",
		Short: "Delete an admin user and sign them out; the last admin can't be deleted",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, users, closeDB, err := openUsers(cmd, cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			if err := users.Delete(ctx, args[0]); err != nil {
				return err
			}
			cliRecord(cmd, cfg, adminlog.AdminDelete, args[0], "")
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted admin user %q. Their sessions were ended.\n", args[0])
			return nil
		},
	}
}
