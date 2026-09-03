package export

import (
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite"
)

// OpenReadOnly opens the SQLite database for reading only. It sets no journal
// mode: WAL is a property of the file, chosen by the daemon that wrote it,
// and a reader that tries to set it on a non-WAL file (a backup made with
// VACUUM INTO, a copy taken with the daemon stopped) fails with "attempt to
// write a readonly database". busy_timeout keeps a concurrent checkpoint from
// surfacing as SQLITE_BUSY; _time_format matches how the daemon binds
// time.Time parameters so window comparisons line up with stored values.
func OpenReadOnly(dbPath string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)&_time_format=sqlite", url.PathEscape(dbPath))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %s: %w", dbPath, err)
	}
	return db, nil
}
