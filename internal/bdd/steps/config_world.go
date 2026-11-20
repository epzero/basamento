package steps

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/epzero/basamento/internal/config"
	"gopkg.in/yaml.v3"
)

type configWorld struct {
	dir      string
	file     map[string]string
	env      []string
	flags    map[string]string
	result   config.Result
	loadErr  error
	errs     []error
	report   string
	dataPath string

	// system command
	kit       *kitProcess
	listen    string
	readyLine string
	holder    net.Listener

	// component serve
	serveCancel   context.CancelFunc
	servePorts    []*fakeClosable
	serveCode     chan int
	serveExit     *int
	shutdown      *shutdownTimer
	inFlight      chan struct{}
	release       chan struct{}
	releaseOnce   sync.Once
	request       chan requestResult
	terminateOnce sync.Once
}

type requestResult struct {
	status int
	err    error
}

type fakeClosable struct {
	mu     sync.Mutex
	name   string
	closed bool
}

func (p *fakeClosable) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

func (p *fakeClosable) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func newConfigWorld() *configWorld {
	return &configWorld{file: map[string]string{}, flags: map[string]string{}}
}

func (w *configWorld) ensureDir() (string, error) {
	if w.dir != "" {
		return w.dir, nil
	}
	dir, err := os.MkdirTemp("", "kit-config-")
	if err != nil {
		return "", err
	}
	w.dir = dir
	return dir, nil
}

func (w *configWorld) writeFile() (string, error) {
	if len(w.file) == 0 {
		return "", nil
	}
	dir, err := w.ensureDir()
	if err != nil {
		return "", err
	}
	root := map[string]any{}
	for key, value := range w.file {
		parts := strings.Split(key, ".")
		m := root
		for _, part := range parts[:len(parts)-1] {
			child, _ := m[part].(map[string]any)
			if child == nil {
				child = map[string]any{}
				m[part] = child
			}
			m = child
		}
		m[parts[len(parts)-1]] = value
	}
	data, err := yaml.Marshal(root)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "kit.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (w *configWorld) options() (config.Options, error) {
	path, err := w.writeFile()
	if err != nil {
		return config.Options{}, err
	}
	return config.Options{File: path, Env: w.env, Flags: w.flags}, nil
}

func (w *configWorld) load() error {
	opts, err := w.options()
	if err != nil {
		return err
	}
	w.result, w.loadErr = config.Load(opts)
	return w.loadErr
}

func (w *configWorld) validate() error {
	if err := w.load(); err != nil {
		return err
	}
	w.errs = w.result.Validate()
	return nil
}

func (w *configWorld) teardown() {
	if w.serveCancel != nil {
		w.serveCancel()
	}
	if w.release != nil {
		w.releaseOnce.Do(func() { close(w.release) })
	}
	if w.serveCode != nil {
		w.serverExit()
	}
	if w.kit != nil {
		w.kit.kill()
	}
	if w.holder != nil {
		_ = w.holder.Close()
	}
	if w.dir != "" {
		_ = os.RemoveAll(w.dir)
	}
}

// kitProcess is a started kit binary whose output is captured line by line.
type kitProcess struct {
	cmd    *exec.Cmd
	mu     sync.Mutex
	out    strings.Builder
	errOut strings.Builder
	ready  chan string
	exited chan struct{}
	code   int
}

func startKit(dir string, args, env []string) (*kitProcess, error) {
	bin, err := kitBinary()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = cleanEnv(env)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	p := &kitProcess{cmd: cmd, ready: make(chan string, 1), exited: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); p.scan(stdout, &p.out, true) }()
	go func() { defer wg.Done(); p.scan(stderr, &p.errOut, false) }()
	go func() {
		wg.Wait()
		err := cmd.Wait()
		p.mu.Lock()
		p.code = exitCode(err)
		p.mu.Unlock()
		close(p.exited)
	}()
	return p, nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func (p *kitProcess) scan(r io.Reader, dst *strings.Builder, markReady bool) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		p.mu.Lock()
		dst.WriteString(line)
		dst.WriteString("\n")
		p.mu.Unlock()
		if markReady && strings.HasPrefix(line, "ready ") {
			select {
			case p.ready <- line:
			default:
			}
		}
	}
}

func (p *kitProcess) stdout() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out.String()
}

func (p *kitProcess) stderr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.errOut.String()
}

func (p *kitProcess) wait() int {
	<-p.exited
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.code
}

func (p *kitProcess) awaitReady() (string, error) {
	select {
	case line := <-p.ready:
		return line, nil
	case <-p.exited:
		return "", fmt.Errorf("kit exited with status %d before ready: %s", p.code, p.stderr())
	}
}

func (p *kitProcess) kill() {
	select {
	case <-p.exited:
		return
	default:
	}
	_ = p.cmd.Process.Kill()
	<-p.exited
}

func cleanEnv(extra []string) []string {
	out := make([]string, 0, len(os.Environ())+len(extra))
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "KIT_") {
			out = append(out, entry)
		}
	}
	return append(out, extra...)
}

func readyField(line, field string) string {
	for _, token := range strings.Fields(line) {
		if value, ok := strings.CutPrefix(token, field+"="); ok {
			return value
		}
	}
	return ""
}

func httpStatus(rawURL string) (int, error) {
	resp, err := http.Get(rawURL)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
