package steps

import (
	"io/fs"
	"testing/fstest"

	"github.com/epzero/basamento/auth/authz"
	"github.com/epzero/basamento/platform/credentials"
)

// combinedAuthMigrations applies credentials (audit) then authz bindings in one goose set.
func combinedAuthMigrations() (fs.FS, error) {
	cred, err := fs.ReadFile(credentials.Migrations(), "00001_credentials.sql")
	if err != nil {
		return nil, err
	}
	bind, err := fs.ReadFile(authz.Migrations(), "00001_authz_bindings.sql")
	if err != nil {
		return nil, err
	}
	return fstest.MapFS{
		"00001_credentials.sql":    &fstest.MapFile{Data: cred},
		"00002_authz_bindings.sql": &fstest.MapFile{Data: bind},
	}, nil
}
