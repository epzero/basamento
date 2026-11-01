// Package embedded runs a Temporal server inside the kit process for the
// embedded profile (WP-L5-03, ADR-0002). Modeled on the Temporal CLI dev server
// (temporalio/cli internal/devserver, MIT): file-backed SQLite through the
// pure-Go modernc driver, one history shard, no UI. Settings are tuned for one
// worker process; see BENCHMARKS.md for the measurements behind them.
//
// It is a separate Go module so that library users of basamento never inherit
// the server's dependency tree.
package embedded

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"go.temporal.io/server/common/cluster"
	"go.temporal.io/server/common/config"
	"go.temporal.io/server/common/dynamicconfig"
	tlog "go.temporal.io/server/common/log"
	"go.temporal.io/server/common/membership/static"
	sqliteplugin "go.temporal.io/server/common/persistence/sql/sqlplugin/sqlite"
	"go.temporal.io/server/common/primitives"
	sqliteschema "go.temporal.io/server/schema/sqlite"
	"go.temporal.io/server/temporal"
)

const host = "127.0.0.1"

// Options configures the embedded server.
type Options struct {
	DBFile    string // empty = in-memory
	Port      int    // frontend gRPC port; 0 = free port
	Namespace string // default "default"
	// TemporalDefaults disables our tuning (WAL, synchronous=normal, four
	// connections, one task-queue partition). Only benchmarks use it, to compare.
	TemporalDefaults bool
}

// Server is a running embedded Temporal server.
type Server struct {
	srv      temporal.Server
	HostPort string
}

// Start starts the server and returns once its services are started.
func Start(o Options) (*Server, error) {
	if o.Namespace == "" {
		o.Namespace = "default"
	}
	if o.Port == 0 {
		o.Port = freePort()
	}
	httpPort := freePort()
	sqlConf := config.SQL{PluginName: sqliteplugin.PluginName}
	if o.DBFile == "" {
		sqlConf.ConnectAttributes = map[string]string{"mode": "memory", "cache": "shared"}
		sqlConf.DatabaseName = fmt.Sprintf("mem%d", time.Now().UnixNano())
	} else {
		sqlConf.ConnectAttributes = map[string]string{"mode": "rwc", "busy_timeout": "10000"}
		if !o.TemporalDefaults {
			sqlConf.ConnectAttributes["journal_mode"] = "wal"
			sqlConf.ConnectAttributes["synchronous"] = "normal"
			sqlConf.MaxConns, sqlConf.MaxIdleConns = 4, 4
		}
		sqlConf.DatabaseName = o.DBFile
		if _, err := os.Stat(o.DBFile); errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(filepath.Dir(o.DBFile), 0o755); err != nil {
				return nil, err
			}
			if err := sqliteschema.SetupSchema(&sqlConf); err != nil {
				return nil, fmt.Errorf("setup schema: %w", err)
			}
		}
	}
	ns, err := sqliteschema.NewNamespaceConfig("active", o.Namespace, false, nil)
	if err != nil {
		return nil, err
	}
	if err := sqliteschema.CreateNamespaces(&sqlConf, ns); err != nil {
		return nil, fmt.Errorf("create namespaces: %w", err)
	}

	var conf config.Config
	conf.Global.Membership.MaxJoinDuration = 30 * time.Second
	conf.Global.Membership.BroadcastAddress = host
	conf.Persistence.DefaultStore = "sqlite-default"
	conf.Persistence.VisibilityStore = "sqlite-default"
	conf.Persistence.NumHistoryShards = 1
	conf.Persistence.DataStores = map[string]config.DataStore{"sqlite-default": {SQL: &sqlConf}}
	conf.ClusterMetadata = &cluster.Config{
		FailoverVersionIncrement: 10,
		MasterClusterName:        "active",
		CurrentClusterName:       "active",
		ClusterInformation: map[string]cluster.ClusterInformation{
			"active": {
				Enabled:                true,
				InitialFailoverVersion: 1,
				RPCAddress:             fmt.Sprintf("%s:%d", host, o.Port),
				HTTPAddress:            fmt.Sprintf("%s:%d", host, httpPort),
				ClusterID:              "basamento-embedded",
			},
		},
	}
	conf.DCRedirectionPolicy.Policy = "noop"
	svc := func(port, http int) config.Service {
		var s config.Service
		s.RPC.GRPCPort, s.RPC.BindOnIP, s.RPC.HTTPPort = port, host, http
		return s
	}
	ports := map[string]int{"frontend": o.Port, "history": freePort(), "matching": freePort(), "worker": freePort()}
	conf.Services = map[string]config.Service{
		"frontend": svc(ports["frontend"], httpPort),
		"history":  svc(ports["history"], 0),
		"matching": svc(ports["matching"], 0),
		"worker":   svc(ports["worker"], 0),
	}
	conf.Archival.History.State = "disabled"
	conf.Archival.Visibility.State = "disabled"
	conf.NamespaceDefaults.Archival.History.State = "disabled"
	conf.NamespaceDefaults.Archival.Visibility.State = "disabled"
	conf.PublicClient.HostPort = fmt.Sprintf("%s:%d", host, o.Port)

	hp := func(name string) static.Hosts { return static.SingleLocalHost(fmt.Sprintf("%s:%d", host, ports[name])) }
	srv, err := temporal.NewServer(
		temporal.WithConfig(&conf),
		temporal.ForServices(temporal.DefaultServices),
		temporal.WithStaticHosts(map[primitives.ServiceName]static.Hosts{
			primitives.FrontendService: hp("frontend"),
			primitives.HistoryService:  hp("history"),
			primitives.MatchingService: hp("matching"),
			primitives.WorkerService:   hp("worker"),
		}),
		temporal.WithLogger(tlog.NewNoopLogger()),
		temporal.WithDynamicConfigClient(dynConfig(o.TemporalDefaults)),
	)
	if err != nil {
		return nil, err
	}
	if err := srv.Start(); err != nil {
		return nil, err
	}
	return &Server{srv: srv, HostPort: fmt.Sprintf("%s:%d", host, o.Port)}, nil
}

// dynConfig uses one task-queue partition unless Temporal's defaults are
// requested: with a single worker process, partitions only add forwarding latency.
func dynConfig(temporalDefaults bool) dynamicconfig.StaticClient {
	c := dynamicconfig.StaticClient{dynamicconfig.HistoryCacheHostLevelMaxSize.Key(): 8096}
	if !temporalDefaults {
		c[dynamicconfig.MatchingNumTaskqueueReadPartitions.Key()] = 1
		c[dynamicconfig.MatchingNumTaskqueueWritePartitions.Key()] = 1
	}
	return c
}

// Stop stops the server.
func (s *Server) Stop() { _ = s.srv.Stop() }

func freePort() int {
	l, err := net.Listen("tcp", host+":0")
	if err != nil {
		panic(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}
