package steps

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/internal/config"
)

func registerConfigServe(sc *godog.ScenarioContext) {
	sc.Step(`^an empty data directory and the port "([^"]*)" free$`, func(ctx context.Context, addr string) error {
		w := world(ctx).Config
		if _, err := w.ensureDir(); err != nil {
			return err
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("port %s is not free: %w", addr, err)
		}
		_ = ln.Close()
		w.listen = addr
		return nil
	})
	sc.Step(`^an empty data directory and a free loopback listener$`, func(ctx context.Context) error {
		w := world(ctx).Config
		if _, err := w.ensureDir(); err != nil {
			return err
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		w.listen = ln.Addr().String()
		return ln.Close()
	})
	sc.Step(`^"kit serve" starts with no other setting$`, func(ctx context.Context) error {
		return world(ctx).Config.startServe("--profile", config.ProfileEmbedded)
	})
	sc.Step(`^"([^"]*)" starts with no other setting$`, func(ctx context.Context, command string) error {
		fields := strings.Fields(command)
		if len(fields) < 1 || fields[0] != "kit" {
			return fmt.Errorf("unsupported command %q", command)
		}
		return world(ctx).Config.startServe(fields[1:]...)
	})
	sc.Step(`^a free loopback listener$`, func(ctx context.Context) error {
		w := world(ctx).Config
		if _, err := w.ensureDir(); err != nil {
			return err
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		w.listen = ln.Addr().String()
		return ln.Close()
	})
	sc.Step(`^"kit serve" starts in it$`, func(ctx context.Context) error {
		return world(ctx).Config.startServe()
	})
	sc.Step(`^"kit serve" starts$`, func(ctx context.Context) error {
		return world(ctx).Config.startServe()
	})
	sc.Step(`^it reports ready$`, func(ctx context.Context) error {
		_, err := world(ctx).Config.awaitReady()
		return err
	})
	sc.Step(`^it reports the listener "([^"]*)"$`, func(ctx context.Context, addr string) error {
		w := world(ctx).Config
		line, err := w.awaitReady()
		if err != nil {
			return err
		}
		if got := readyField(line, "listen"); got != addr {
			return fmt.Errorf("ready listener = %q, want %q", got, addr)
		}
		return nil
	})
	sc.Step(`^the ready line reports the "([^"]*)", "([^"]*)", "([^"]*)" and "([^"]*)" implementations$`, func(ctx context.Context, a, b, c, d string) error {
		w := world(ctx).Config
		line, err := w.awaitReady()
		if err != nil {
			return err
		}
		for _, name := range []string{a, b, c, d} {
			if readyField(line, name) == "" {
				return fmt.Errorf("ready line %q does not report %s", line, name)
			}
		}
		return nil
	})
	sc.Step(`^its health check on "([^"]*)" answers "([^"]*)"$`, func(ctx context.Context, addr, want string) error {
		code, err := httpStatus("http://" + addr + "/healthz")
		if err != nil {
			return err
		}
		if fmt.Sprint(code) != want {
			return fmt.Errorf("healthz = %d, want %s", code, want)
		}
		return nil
	})
	sc.Step(`^it answers the health check on its listener with "([^"]*)"$`, func(ctx context.Context, want string) error {
		w := world(ctx).Config
		addr := w.listen
		if line, err := w.awaitReady(); err == nil {
			if got := readyField(line, "listen"); got != "" {
				addr = got
			}
		}
		code, err := httpStatus("http://" + addr + "/healthz")
		if err != nil {
			return err
		}
		if fmt.Sprint(code) != want {
			return fmt.Errorf("healthz = %d, want %s", code, want)
		}
		return nil
	})
	sc.Step(`^a data directory that cannot be written$`, func(ctx context.Context) error {
		w := world(ctx).Config
		dir, err := w.ensureDir()
		if err != nil {
			return err
		}
		path := filepath.Join(dir, "not-a-directory")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			return err
		}
		w.dataPath = path
		w.file["data.dir"] = path
		return nil
	})
	sc.Step(`^it exits with status (\d+)$`, func(ctx context.Context, want int) error {
		w := world(ctx).Config
		switch {
		case w.kit != nil:
			if got := w.kit.wait(); got != want {
				return fmt.Errorf("exit status = %d, want %d: %s", got, want, w.kit.stderr())
			}
		case w.serveCode != nil:
			if got := w.serverExit(); got != want {
				return fmt.Errorf("exit status = %d, want %d", got, want)
			}
		default:
			return fmt.Errorf("nothing was started")
		}
		return nil
	})
	sc.Step(`^it exits with status (\d+) and reports that port "([^"]*)" could not open$`, func(ctx context.Context, want int, port string) error {
		w := world(ctx).Config
		if got := w.kit.wait(); got != want {
			return fmt.Errorf("exit status = %d, want %d: %s", got, want, w.kit.stderr())
		}
		if needle := `port "` + port + `" could not open`; !strings.Contains(w.kit.stderr(), needle) {
			return fmt.Errorf("stderr does not report %q:\n%s", needle, w.kit.stderr())
		}
		return nil
	})
	sc.Step(`^it opens no listener$`, func(ctx context.Context) error {
		w := world(ctx).Config
		addr := w.listen
		if addr == "" {
			addr = "127.0.0.1:8080"
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("something is still listening on %s: %w", addr, err)
		}
		return ln.Close()
	})
	sc.Step(`^it exits with status (\d+) and reports the (\d+) errors? naming "([^"]*)"$`, func(ctx context.Context, want, n int, key string) error {
		w := world(ctx).Config
		if got := w.kit.wait(); got != want {
			return fmt.Errorf("exit status = %d, want %d: %s", got, want, w.kit.stderr())
		}
		keys := errorKeysIn(w.kit.stderr())
		if len(keys) != n {
			return fmt.Errorf("got %d errors (%v), want %d", len(keys), keys, n)
		}
		for _, got := range keys {
			if got == key {
				return nil
			}
		}
		return fmt.Errorf("errors name %v, want %q", keys, key)
	})
	sc.Step(`^it opens no listener and creates nothing in the data directory$`, func(ctx context.Context) error {
		w := world(ctx).Config
		addr := w.listen
		if addr == "" {
			addr = "127.0.0.1:8080"
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("something is still listening on %s: %w", addr, err)
		}
		_ = ln.Close()
		dataDir := filepath.Join(w.dir, ".kit", "data")
		if w.file["data.dir"] != "" {
			dataDir = w.file["data.dir"]
		}
		entries, err := os.ReadDir(dataDir)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return fmt.Errorf("data directory %s has %d entries", dataDir, len(entries))
		}
		return nil
	})
	sc.Step(`^another process already listens on "([^"]*)"$`, func(ctx context.Context, addr string) error {
		w := world(ctx).Config
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return err
		}
		w.holder = ln
		return nil
	})
	sc.Step(`^it reports that "([^"]*)" could not be bound$`, func(ctx context.Context, key string) error {
		w := world(ctx).Config
		if !strings.Contains(w.kit.stderr(), key) || !strings.Contains(w.kit.stderr(), "could not be bound") {
			return fmt.Errorf("stderr does not report %s could not be bound:\n%s", key, w.kit.stderr())
		}
		return nil
	})
	sc.Step(`^it never reports ready$`, func(ctx context.Context) error {
		w := world(ctx).Config
		if strings.Contains(w.kit.stdout(), "ready ") {
			return fmt.Errorf("kit reported ready:\n%s", w.kit.stdout())
		}
		return nil
	})

	sc.Step(`^"kit config validate" runs$`, func(ctx context.Context) error {
		w := world(ctx).Config
		dir, err := w.ensureDir()
		if err != nil {
			return err
		}
		if _, err := w.writeFile(); err != nil {
			return err
		}
		w.kit, err = startKit(dir, []string{"config", "validate"}, w.env)
		return err
	})
	sc.Step(`^it prints "([^"]*)" as "([^"]*)" from "([^"]*)"$`, func(ctx context.Context, key, want, source string) error {
		w := world(ctx).Config
		for _, line := range strings.Split(w.kit.stdout(), "\n") {
			if valueKey(line) != key {
				continue
			}
			value, src, ok := reportLine(line)
			if !ok {
				continue
			}
			if value != want || src != source {
				return fmt.Errorf("%s printed as %q from %q, want %q from %q", key, value, src, want, source)
			}
			return nil
		}
		return fmt.Errorf("output has no line for %s:\n%s", key, w.kit.stdout())
	})
	sc.Step(`^it prints (\d+) errors? naming "([^"]*)" and "([^"]*)"$`, func(ctx context.Context, n int, a, b string) error {
		keys := errorKeysIn(world(ctx).Config.kit.stderr())
		if len(keys) != n {
			return fmt.Errorf("got %d errors (%v), want %d", len(keys), keys, n)
		}
		want := map[string]bool{a: true, b: true}
		for _, key := range keys {
			delete(want, key)
		}
		if len(want) != 0 {
			return fmt.Errorf("errors %v do not name %v", keys, want)
		}
		return nil
	})

	sc.Step(`^the server is ready with "([^"]*)" set to "([^"]*)"$`, func(ctx context.Context, key, value string) error {
		w := world(ctx).Config
		if key != "shutdown.timeout" {
			return fmt.Errorf("unsupported component setting %q", key)
		}
		timeout, err := time.ParseDuration(value)
		if err != nil {
			return err
		}
		w.after = make(chan time.Time, 1)
		w.inFlight = make(chan struct{})
		w.release = make(chan struct{})
		w.request = make(chan requestResult, 1)
		var once sync.Once
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(rw http.ResponseWriter, _ *http.Request) {
			rw.WriteHeader(http.StatusOK)
		})
		mux.HandleFunc("/slow", func(rw http.ResponseWriter, _ *http.Request) {
			once.Do(func() { close(w.inFlight) })
			<-w.release
			rw.WriteHeader(http.StatusOK)
		})
		ctx, cancel := context.WithCancel(context.Background())
		w.serveCancel = cancel
		for _, name := range []string{config.PortSQL, config.PortBlob, config.PortBus, config.PortSecrets} {
			w.servePorts = append(w.servePorts, &fakeClosable{name: name})
		}
		ports := make([]config.Port, len(w.servePorts))
		for i := range w.servePorts {
			ports[i] = w.servePorts[i]
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		srv := config.NewServer(ln, mux, ports, timeout, channelTimer{ch: w.after})
		w.serveCode = make(chan int, 1)
		go func() { w.serveCode <- srv.Serve(ctx) }()
		w.listen = ln.Addr().String()
		return nil
	})
	sc.Step(`^a request is in flight$`, func(ctx context.Context) error {
		w := world(ctx).Config
		go func() {
			code, err := httpStatus("http://" + w.listen + "/slow")
			w.request <- requestResult{status: code, err: err}
		}()
		select {
		case <-w.inFlight:
			return nil
		case result := <-w.request:
			if result.err != nil {
				return fmt.Errorf("in-flight request failed: %w", result.err)
			}
			return fmt.Errorf("in-flight request returned %d without blocking", result.status)
		}
	})
	sc.Step(`^the server receives a termination signal$`, func(ctx context.Context) error {
		w := world(ctx).Config
		if w.serveCancel == nil {
			return fmt.Errorf("the server is not running")
		}
		w.terminateOnce.Do(w.serveCancel)
		return nil
	})
	sc.Step(`^the in-flight request completes before "([^"]*)" elapses$`, func(ctx context.Context, _ string) error {
		w := world(ctx).Config
		w.releaseOnce.Do(func() { close(w.release) })
		return nil
	})
	sc.Step(`^the request receives its response$`, func(ctx context.Context) error {
		w := world(ctx).Config
		result := <-w.request
		if result.err != nil {
			return result.err
		}
		if result.status != http.StatusOK {
			return fmt.Errorf("request status = %d, want 200", result.status)
		}
		return nil
	})
	sc.Step(`^new requests are refused$`, func(ctx context.Context) error {
		w := world(ctx).Config
		if w.serveCode == nil {
			return fmt.Errorf("the server is not running")
		}
		// Wait for the listener to close before probing it.
		w.serverExit()
		if resp, err := http.Get("http://" + w.listen + "/healthz"); err == nil {
			_ = resp.Body.Close()
			return fmt.Errorf("the server still accepts requests")
		}
		return nil
	})
	sc.Step(`^every port is closed$`, func(ctx context.Context) error {
		w := world(ctx).Config
		if w.serveCode != nil {
			w.serverExit()
		}
		for _, port := range w.servePorts {
			if !port.isClosed() {
				return fmt.Errorf("port %q is not closed", port.name)
			}
		}
		return nil
	})
	sc.Step(`^the server exits with status (\d+)$`, func(ctx context.Context, want int) error {
		w := world(ctx).Config
		if w.serveCode == nil {
			return fmt.Errorf("the server is not running")
		}
		if got := w.serverExit(); got != want {
			return fmt.Errorf("exit status = %d, want %d", got, want)
		}
		return nil
	})
}

func (w *configWorld) startServe(extra ...string) error {
	dir, err := w.ensureDir()
	if err != nil {
		return err
	}
	if _, err := w.writeFile(); err != nil {
		return err
	}
	args := []string{"serve"}
	args = append(args, extra...)
	if w.listen != "" && w.file["listen.addr"] == "" && len(extra) == 0 {
		args = append(args, "--listen.addr", w.listen)
	}
	w.kit, err = startKit(dir, args, w.env)
	return err
}

func (w *configWorld) serverExit() int {
	if w.serveExit == nil {
		code := <-w.serveCode
		w.serveExit = &code
	}
	return *w.serveExit
}

func (w *configWorld) awaitReady() (string, error) {
	if w.readyLine != "" {
		return w.readyLine, nil
	}
	if w.kit == nil {
		return "", fmt.Errorf("kit serve is not running")
	}
	line, err := w.kit.awaitReady()
	if err == nil {
		w.readyLine = line
	}
	return line, err
}

var settingKey = regexp.MustCompile(`invalid setting "([^"]*)"`)

func errorKeysIn(text string) []string {
	var keys []string
	for _, match := range settingKey.FindAllStringSubmatch(text, -1) {
		keys = append(keys, match[1])
	}
	return keys
}

type channelTimer struct{ ch chan time.Time }

func (c channelTimer) After(time.Duration) <-chan time.Time { return c.ch }
