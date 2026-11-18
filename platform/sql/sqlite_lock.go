package sql

import "sync"

var sqliteMigrateLocks sync.Map // sqlite file path -> *sync.Mutex

func sqliteMigrateMutex(path string) *sync.Mutex {
	v, _ := sqliteMigrateLocks.LoadOrStore(path, &sync.Mutex{})
	return v.(*sync.Mutex)
}
