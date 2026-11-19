package bus

import "testing"

func TestEmbeddedServerOptions(t *testing.T) {
	o := embeddedServerOptions(t.TempDir(), 1024)
	if !o.DontListen {
		t.Fatal("DontListen")
	}
	if o.HTTPPort != 0 || o.HTTPSPort != 0 || o.ProfPort != 0 {
		t.Fatalf("monitor ports http=%d https=%d prof=%d", o.HTTPPort, o.HTTPSPort, o.ProfPort)
	}
	if o.Cluster.Port != 0 || o.LeafNode.Port != 0 || o.Websocket.Port != 0 || o.MQTT.Port != 0 {
		t.Fatal("extra listeners configured")
	}
}

func TestStartedEmbeddedHasNoPublicListeners(t *testing.T) {
	srv, err := startEmbeddedServer(t.TempDir(), 2048)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		srv.Shutdown()
		srv.WaitForShutdown()
	}()
	if srv.Addr() != nil {
		t.Fatalf("client addr %v", srv.Addr())
	}
	if srv.MonitorAddr() != nil {
		t.Fatalf("monitor addr %v", srv.MonitorAddr())
	}
	if srv.ProfilerAddr() != nil {
		t.Fatalf("profiler addr %v", srv.ProfilerAddr())
	}
	if srv.ClusterAddr() != nil {
		t.Fatalf("cluster addr %v", srv.ClusterAddr())
	}
}
