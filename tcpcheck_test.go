package main

import (
	"net"
	"testing"
	"time"
)

func TestTCPCollectorReportsHealthyAndUnhealthy(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	go func() {
		conn, err := listener.Accept()
		if err == nil {
			conn.Close()
		}
	}()

	samples := tcpCollector([]TCPCheckConfig{
		{Name: "db", Address: listener.Addr().String(), Timeout: Duration(time.Second)},
		{Name: "missing db", Address: "127.0.0.1:1", Timeout: Duration(100 * time.Millisecond)},
	})

	values := metricValues(samples)
	if values["tcp.db.unhealthy"] != 0 {
		t.Fatalf("healthy tcp check reported unhealthy: %+v", samples)
	}
	if values["tcp.missing_db.unhealthy"] != 1 {
		t.Fatalf("unhealthy tcp check did not report unhealthy: %+v", samples)
	}
}

func TestTCPCollectorReportsMissingAddressUnhealthy(t *testing.T) {
	samples := tcpCollector([]TCPCheckConfig{{Name: "bad"}})
	values := metricValues(samples)
	if values["tcp.bad.unhealthy"] != 1 {
		t.Fatalf("missing address should be unhealthy: %+v", samples)
	}
}
