package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Beginner starts transactions. *pgxpool.Pool satisfies it.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// TxRunner runs a use case inside one transaction (GO-45, ARC-06).
type TxRunner struct {
	db Beginner
}

// NewTxRunner returns a TxRunner over db.
func NewTxRunner(db Beginner) (*TxRunner, error) {
	if db == nil {
		return nil, errors.New("tx runner: database is required")
	}
	return &TxRunner{db: db}, nil
}

// WithTx runs fn in a transaction. It commits if fn returns nil and rolls back otherwise,
// including when fn panics (the panic is re-raised after the rollback).
func (r *TxRunner) WithTx(ctx context.Context, fn func(tx pgx.Tx) error) (err error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx)) // Best effort; the panic is the real error.
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}
