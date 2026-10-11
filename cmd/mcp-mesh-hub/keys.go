package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/acl"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/adminlog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/api"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/auth"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
)

// keyCmd manages the keys in the hub database, which the running hub watches,
// so a new or revoked key takes effect without a restart.
func keyCmd(cfg *config) *cobra.Command {
	key := &cobra.Command{
		Use:   "key",
		Short: "Create, list and revoke node and client keys",
		Long: "Create, list and revoke node and client keys. Dashboard sign-in uses admin users (mcp-mesh-hub admin), not keys.\n\n" +
			"Keys live in <data-dir>/hub.db. A running hub picks up changes within a second.",
	}
	key.AddCommand(keyCreateCmd(cfg), keyListCmd(cfg), keyRevokeCmd(cfg))
	return key
}

// openDB opens the hub database. The caller calls the returned func to close it.
func openDB(cmd *cobra.Command, cfg *config) (context.Context, *store.DB, func(), error) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := store.Open(ctx, cfg.DBPath(), nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open database: %w", err)
	}
	return ctx, db, func() { db.Close() }, nil
}

// cliRecord writes an admin log entry for a change made from the command
// line. A failure only warns: the change itself already happened.
func cliRecord(cmd *cobra.Command, cfg *config, action, target, detail string) {
	ctx, db, closeDB, err := openDB(cmd, cfg)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: admin log not written: %v\n", err)
		return
	}
	defer closeDB()
	adminlog.New(db, nil, 0).Record(ctx, adminlog.Entry{Actor: "cli", Action: action, Target: target, Detail: detail})
}

// joinDefaultGroup puts a new client key in the Everything group, as the
// dashboard does: access is default deny, so a key in no group sees nothing.
func joinDefaultGroup(cmd *cobra.Command, cfg *config, keyID string) error {
	ctx, db, closeDB, err := openDB(cmd, cfg)
	if err != nil {
		return err
	}
	defer closeDB()
	groups, err := acl.Open(ctx, db, acl.Options{})
	if err != nil {
		return err
	}
	if _, err := groups.Get(ctx, acl.Everything); errors.Is(err, acl.ErrNotFound) {
		fmt.Fprintln(cmd.ErrOrStderr(), "note: the Everything group was deleted, so this key is in no group and sees no tools until you add it to one in the dashboard")
		return nil
	}
	return groups.SetKeyGroups(ctx, keyID, []string{acl.Everything})
}

// openStore opens the hub database and its keys. The caller calls the
// returned func to close the database.
func openStore(cmd *cobra.Command, cfg *config) (*auth.Store, func(), error) {
	ctx, db, closeDB, err := openDB(cmd, cfg)
	if err != nil {
		return nil, nil, err
	}
	keys, err := auth.Open(ctx, db, nil)
	if err != nil {
		closeDB()
		return nil, nil, fmt.Errorf("open keys: %w", err)
	}
	return keys, closeDB, nil
}

func keyCreateCmd(cfg *config) *cobra.Command {
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a key; the secret is shown once",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			if args[0] == "admin" {
				return errors.New("dashboard admins sign in with a password, not a key: use mcp-mesh-hub admin create <username>")
			}
			return fmt.Errorf("unknown key kind %q (want node or client)", args[0])
		},
	}
	for _, k := range []struct {
		kind auth.Kind
		arg  string
		desc string
	}{
		{auth.KindNode, "<node-name>", "Create a node key, bound to one node name"},
		{auth.KindClient, "<label>", "Create a client key for an agent or device"},
	} {
		create.AddCommand(&cobra.Command{
			Use:   string(k.kind) + " " + k.arg,
			Short: k.desc,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				keys, closeDB, err := openStore(cmd, cfg)
				if err != nil {
					return err
				}
				defer closeDB()
				secret, created, err := keys.Create(k.kind, args[0])
				if err != nil {
					return err
				}
				if k.kind == auth.KindClient {
					if err := joinDefaultGroup(cmd, cfg, created.ID); err != nil {
						return fmt.Errorf("key %s created, but adding it to the Everything group failed: %w", created.ID, err)
					}
				}
				cliRecord(cmd, cfg, adminlog.KeyCreate, created.ID, string(created.Kind)+" "+created.Name)
				nodeURL, clientURL := advertisedURLs(cmd, cfg)
				printCreated(cmd.OutOrStdout(), nodeURL, clientURL, created, secret)
				return nil
			},
		})
	}
	return create
}

// advertisedURLs are the node and client URLs to print: the ones the
// running hub recorded when it started, unless this command was given its
// own (flags or environment), since the CLI doesn't see the hub's flags.
func advertisedURLs(cmd *cobra.Command, cfg *config) (nodeURL, clientURL string) {
	_, nodeURL, clientURL = cfg.URLs(cfg.Ports())
	for _, f := range []struct{ flag, env string }{
		{"public-host", "MCPMESH_PUBLIC_HOST"}, {"node-url", "MCPMESH_NODE_URL"},
		{"client-url", "MCPMESH_CLIENT_URL"}, {"tls-cert", "MCPMESH_TLS_CERT"},
	} {
		if cmd.Flags().Changed(f.flag) || os.Getenv(f.env) != "" {
			return nodeURL, clientURL
		}
	}
	ctx, db, closeDB, err := openDB(cmd, cfg)
	if err != nil {
		return nodeURL, clientURL
	}
	defer closeDB()
	n, okN, errN := db.Setting(ctx, store.SettingNodeURL)
	c, okC, errC := db.Setting(ctx, store.SettingClientURL)
	if errN == nil && errC == nil && okN && okC {
		return n, c
	}
	return nodeURL, clientURL
}

// printCreated shows a new key's secret once, with what to run where.
func printCreated(w io.Writer, nodeURL, clientURL string, key auth.Key, secret string) {
	fmt.Fprintf(w, "Created %s key %s for %q.\n\n", key.Kind, key.ID, key.Name)
	fmt.Fprintf(w, "Secret (shown once, store it now):\n\n  %s\n\n", secret)
	switch key.Kind {
	case auth.KindNode:
		fmt.Fprintf(w, "On a new machine, this sets up the node %q:\n\n  %s\n\n", key.Name, api.NodeInitCommand(nodeURL, key.Name, secret))
		fmt.Fprintf(w, "On a machine that is already set up as %q:\n\n  mcp-mesh-node key set %s\n\n", key.Name, secret)
	case auth.KindClient:
		fmt.Fprintf(w, "Claude Code:\n\n  claude mcp add --transport http mcp-mesh %s --header \"Authorization: Bearer %s\"\n\n", clientURL, secret)
		fmt.Fprintf(w, "Other clients (mcpServers JSON):\n\n")
		snippet, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{"mcp-mesh": map[string]any{
			"type": "http", "url": clientURL, "headers": map[string]string{"Authorization": "Bearer " + secret},
		}}}, "  ", "  ")
		fmt.Fprintf(w, "  %s\n\n", snippet)
	}
	fmt.Fprintln(w, "A running hub picks the new key up within a second.")
}

type keyJSON struct {
	ID      string    `json:"id"`
	Kind    auth.Kind `json:"kind"`
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
}

func keyListCmd(cfg *config) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List keys (never their secrets)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, closeDB, err := openStore(cmd, cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			keys, err := store.List()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				list := make([]keyJSON, len(keys))
				for i, k := range keys {
					list[i] = keyJSON{k.ID, k.Kind, k.Name, k.Created}
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(list)
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tKIND\tNAME\tCREATED")
			for _, k := range keys {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", k.ID, k.Kind, k.Name, k.Created.Local().Format("2006-01-02 15:04"))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func keyRevokeCmd(cfg *config) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <id|name>",
		Short: "Revoke a key; a running hub applies it within a second",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			keys, closeDB, err := openStore(cmd, cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			key, err := keys.Revoke(args[0])
			if err != nil {
				return err
			}
			cliRecord(cmd, cfg, adminlog.KeyRevoke, key.ID, string(key.Kind)+" "+key.Name)
			fmt.Fprintf(cmd.OutOrStdout(), "Revoked %s key %s (%s). A running hub applies this within a second.\n", key.Kind, key.ID, key.Name)
			return nil
		},
	}
}
