package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The URLs a running hub advertises, recorded at start so the hub's CLI
// (another process, without the hub's flags) prints the same ones.
const (
	SettingAdminURL  = "hub.admin_url"
	SettingNodeURL   = "hub.node_url"
	SettingClientURL = "hub.client_url"
)

// Setting returns a value from the settings table; ok is false when the
// key has none.
func (db *DB) Setting(ctx context.Context, key string) (value string, ok bool, err error) {
	err = db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read setting %s: %w", key, err)
	}
	return value, true, nil
}

// SetSettings writes several settings in one transaction.
func (db *DB) SetSettings(ctx context.Context, values map[string]string) error {
	return db.Tx(ctx, func(tx *sql.Tx) error {
		for k, v := range values {
			if _, err := tx.ExecContext(ctx, "INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", k, v); err != nil {
				return fmt.Errorf("write setting %s: %w", k, err)
			}
		}
		return nil
	})
}
