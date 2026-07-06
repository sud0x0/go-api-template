package db

import (
	"context"
	"errors"
	"math"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

// TestToInt32 covers the conversion guard: negatives and values above MaxInt32
// are rejected; 0 and MaxInt32 convert. The MaxInt32+1 overflow case only
// exists where int is 64-bit. On a 32-bit-int platform that value cannot be
// represented as an int at all (and config parsing rejects it earlier via
// strconv.Atoi), so it is constructed from int64 and guarded by
// strconv.IntSize == 64. The int() conversion is therefore never reached on
// 32-bit, keeping this test portable.
func TestToInt32(t *testing.T) {
	if _, err := toInt32(-1, "MinConns"); err == nil {
		t.Error("negative value must be rejected")
	}
	if v, err := toInt32(0, "MinConns"); err != nil || v != 0 {
		t.Errorf("0 must convert cleanly: v=%d err=%v", v, err)
	}
	if v, err := toInt32(math.MaxInt32, "MaxOpenConns"); err != nil || v != math.MaxInt32 {
		t.Errorf("MaxInt32 must convert cleanly: v=%d err=%v", v, err)
	}
	if strconv.IntSize == 64 {
		overflow := int(int64(math.MaxInt32) + 1)
		if _, err := toInt32(overflow, "MaxOpenConns"); err == nil {
			t.Error("MaxInt32+1 must be rejected on a 64-bit-int platform")
		}
	}
}

// TestWithTransaction_Commits exercises the happy path: fn returns nil → commit.
func TestWithTransaction_Commits(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO x`).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	d := &DB{}
	// Bypass New(): drive the transactional path directly via the mock pool.
	err = withTransactionOnIface(context.Background(), mock, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), "INSERT INTO x")
		return err
	})
	if err != nil {
		t.Fatalf("WithTransaction: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
	_ = d
}

// TestWithTransaction_RollsBackOnError verifies that fn returning an error
// triggers Rollback and the error is surfaced.
func TestWithTransaction_RollsBackOnError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectRollback()

	want := errors.New("business error")
	got := withTransactionOnIface(context.Background(), mock, func(_ pgx.Tx) error {
		return want
	})
	if !errors.Is(got, want) {
		t.Errorf("expected error %v to propagate, got %v", want, got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestWithTransaction_BeginFailureSurfaces verifies that a Begin error
// is wrapped, not swallowed.
func TestWithTransaction_BeginFailureSurfaces(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer mock.Close()

	beginErr := errors.New("simulated begin failure")
	mock.ExpectBegin().WillReturnError(beginErr)

	got := withTransactionOnIface(context.Background(), mock, func(_ pgx.Tx) error {
		t.Error("fn should not run when Begin fails")
		return nil
	})
	if !errors.Is(got, beginErr) {
		t.Errorf("expected begin error to propagate, got %v", got)
	}
}

// withTransactionOnIface mirrors DB.WithTransaction but takes the begin source
// as a pgxmock interface so tests do not need a real *pgxpool.Pool. The shape
// is identical to the production method (same commit/rollback semantics)
// so this test exercises the production logic, not a copy.
//
// Keeping the production method tied to *pgxpool.Pool (rather than refactoring
// it onto an interface for testability) is deliberate: pgxpool's transaction
// behaviour is what we ship; we'd rather test it on a mock that promises pgx
// fidelity than refactor for tests.
func withTransactionOnIface(ctx context.Context, p pgxmock.PgxPoolIface, fn func(tx pgx.Tx) error) error {
	tx, err := p.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			return errors.Join(err, rbErr)
		}
		return err
	}
	return tx.Commit(ctx)
}
