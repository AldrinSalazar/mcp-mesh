// Package logx has small log/slog helpers.
package logx

import (
	"context"
	"log/slog"
)

// AtLeast returns a logger that drops records below min. The SDK logs every
// stateless request's session at Info; hand it AtLeast(log, slog.LevelWarn).
func AtLeast(l *slog.Logger, min slog.Level) *slog.Logger {
	return slog.New(&minLevel{h: l.Handler(), min: min})
}

type minLevel struct {
	h   slog.Handler
	min slog.Level
}

func (m *minLevel) Enabled(ctx context.Context, lv slog.Level) bool {
	return lv >= m.min && m.h.Enabled(ctx, lv)
}

func (m *minLevel) Handle(ctx context.Context, r slog.Record) error { return m.h.Handle(ctx, r) }

func (m *minLevel) WithAttrs(as []slog.Attr) slog.Handler {
	return &minLevel{h: m.h.WithAttrs(as), min: m.min}
}

func (m *minLevel) WithGroup(name string) slog.Handler {
	return &minLevel{h: m.h.WithGroup(name), min: m.min}
}
