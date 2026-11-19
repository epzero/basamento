//go:build !unix

package sql

import "os"

func chmodSQLiteDBFile(dbPath string) error {
	return os.Chmod(dbPath, 0o600)
}

func createSQLiteDBFile(dbPath string) error {
	f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}
