package store

import (
	"context"
	"database/sql"
)

type Transactor interface {
	WithinTx(context.Context, func(context.Context) error) error
}

type NoopTransactor struct{}

func (NoopTransactor) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

type txKey struct{}

func (h *Handle) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	if txFromContext(ctx) != nil {
		return fn(ctx)
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	txCtx := context.WithValue(ctx, txKey{}, tx)
	if err := fn(txCtx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func txFromContext(ctx context.Context) *sql.Tx {
	tx, _ := ctx.Value(txKey{}).(*sql.Tx)
	return tx
}

func (h *Handle) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if tx := txFromContext(ctx); tx != nil {
		return tx.ExecContext(ctx, query, args...)
	}
	return h.db.ExecContext(ctx, query, args...)
}

func (h *Handle) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if tx := txFromContext(ctx); tx != nil {
		return tx.QueryContext(ctx, query, args...)
	}
	return h.db.QueryContext(ctx, query, args...)
}

func (h *Handle) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	if tx := txFromContext(ctx); tx != nil {
		return tx.QueryRowContext(ctx, query, args...)
	}
	return h.db.QueryRowContext(ctx, query, args...)
}
