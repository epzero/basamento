package sim

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/epzero/basamento/model/providers"
)

// CLI is a simulated command-line provider. It writes its configured files
// into the command's working directory, prints its configured progress lines
// and exits with its configured error. It implements providers.Runner.
type CLI struct {
	// Files are written relative to the command's working directory before
	// the command "runs".
	Files map[string][]byte
	// Lines are printed to stdout, one per line.
	Lines []string
	// Err is returned by Wait. Nil is success.
	Err error
}

// Start writes the files and returns a process that prints the configured
// lines.
func (c *CLI) Start(_ context.Context, cmd providers.Command) (providers.Process, error) {
	for name, data := range c.Files {
		path := name
		if !filepath.IsAbs(path) {
			path = filepath.Join(cmd.Dir, name)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, err
		}
	}
	return &cliProcess{out: strings.NewReader(strings.Join(c.Lines, "\n") + "\n"), err: c.Err}, nil
}

type cliProcess struct {
	out io.Reader
	err error
}

func (p *cliProcess) Stdout() io.Reader { return p.out }
func (p *cliProcess) Wait() error       { return p.err }
