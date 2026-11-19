package sql

import (
	"errors"
	"strings"

	"github.com/epzero/basamento/core"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// sqliteRetryable reports whether err is a SQLite busy or locked condition
// (SQLITE_BUSY, SQLITE_LOCKED and their extended codes).
func sqliteRetryable(err error) bool {
	if err == nil || errorsAsConnectError(err) {
		return false
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_BUSY_RECOVERY, sqlite3.SQLITE_BUSY_SNAPSHOT,
			sqlite3.SQLITE_BUSY_TIMEOUT, sqlite3.SQLITE_LOCKED, sqlite3.SQLITE_LOCKED_SHAREDCACHE,
			sqlite3.SQLITE_LOCKED_VTAB:
			return true
		}
	}
	s := strings.ToUpper(safeErrString(err))
	return strings.Contains(s, "SQLITE_BUSY") ||
		strings.Contains(s, "SQLITE_LOCKED") ||
		strings.Contains(s, "DATABASE IS LOCKED") ||
		strings.Contains(s, "DATABASE TABLE IS LOCKED")
}

// sqliteConstraintClass maps a SQLite constraint violation to a platform code.
// Not-null and check violations are invalid input (REQ-SQL-022); uniqueness and
// foreign-key violations are conflicts (REQ-SQL-014, REQ-SQL-015). The bool is
// false when err is not a constraint violation.
func sqliteConstraintClass(err error) (core.Code, bool) {
	if err == nil || errorsAsConnectError(err) {
		return "", false
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case sqlite3.SQLITE_CONSTRAINT_NOTNULL, sqlite3.SQLITE_CONSTRAINT_CHECK, sqlite3.SQLITE_CONSTRAINT_DATATYPE:
			return core.CodeInvalidArgument, true
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY,
			sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY, sqlite3.SQLITE_CONSTRAINT_ROWID, sqlite3.SQLITE_CONSTRAINT:
			return core.CodeConflict, true
		}
	}
	s := strings.ToUpper(safeErrString(err))
	switch {
	case strings.Contains(s, "NOT NULL CONSTRAINT"), strings.Contains(s, "CHECK CONSTRAINT"):
		return core.CodeInvalidArgument, true
	case strings.Contains(s, "UNIQUE CONSTRAINT"), strings.Contains(s, "FOREIGN KEY CONSTRAINT"),
		strings.Contains(s, "CONSTRAINT FAILED"):
		return core.CodeConflict, true
	}
	return "", false
}

// sqliteResultCode returns the driver result code for details, or 0.
func sqliteResultCode(err error) int {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code()
	}
	return 0
}
