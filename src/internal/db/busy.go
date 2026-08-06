package db

import (
	"context"
	"errors"
	"time"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Retry schedule for lock contention. busy_timeout already makes writers wait
// up to 5s for a held lock, so reaching this path means the wait itself timed
// out (or the transaction hit a snapshot conflict). A short, bounded retry
// covers the burst; anything longer is a real problem the caller should see.
var busyRetryDelays = []time.Duration{
	5 * time.Millisecond,
	20 * time.Millisecond,
	80 * time.Millisecond,
	200 * time.Millisecond,
}

// isBusy reports whether err is SQLite's "database is locked"/"table is locked"
// family, including the SQLITE_BUSY_SNAPSHOT extended code raised when a
// deferred transaction cannot upgrade its read snapshot to a write.
func isBusy(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	switch se.Code() {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED,
		sqlite3.SQLITE_BUSY_SNAPSHOT, sqlite3.SQLITE_BUSY_RECOVERY, sqlite3.SQLITE_BUSY_TIMEOUT:
		return true
	}
	return false
}

// retryOnBusy runs op, retrying while SQLite reports the database as locked.
//
// op must be a whole transaction, not a fragment of one: a busy failure leaves
// nothing committed (SQLite commits atomically), so re-running from the top is
// safe and re-reads the state each time. Guard-clause transactions stay correct
// under retry — if a competing writer won the race, the retry's own SELECT sees
// the new state and returns the same ErrStaleClaim it would have returned
// without contention.
//
// This is the second line of defence. The first is _txlock=immediate (see the
// DSN in New), which stops read-then-write transactions from failing on a stale
// snapshot in the first place.
func retryOnBusy(ctx context.Context, op func() error) error {
	err := op()
	for attempt := 0; isBusy(err) && attempt < len(busyRetryDelays); attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(busyRetryDelays[attempt]):
		}
		err = op()
	}
	return err
}
