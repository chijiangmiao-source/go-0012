package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"offshore-buoy-drift-search-loop/internal/domain"
	"offshore-buoy-drift-search-loop/internal/drift"
	"offshore-buoy-drift-search-loop/internal/fleet"
	"offshore-buoy-drift-search-loop/internal/store"
)

func TestModel_RestartRecoveryPrecedesPublicReadsAndScheduling(t *testing.T) {
	if os.Getenv("TEST_MODEL_RESTART_SERVER") == "1" {
		os.Args = []string{
			"offshore-buoy", "server",
			"-addr", os.Getenv("TEST_MODEL_RESTART_ADDR"),
			"-sqlite", os.Getenv("TEST_MODEL_RESTART_DB"),
			"-heartbeat-timeout", "5m",
			"-inspection-period", "24h",
		}
		main()
		return
	}

	cases := []struct {
		name           string
		vesselNo       string
		initialStatus  string
		heartbeatAge   time.Duration
		wantOnline     bool
		wantVersion    int64
		wantAssignable bool
	}{
		{name: "expired online vessel", vesselNo: "expired", initialStatus: "online", heartbeatAge: time.Hour, wantOnline: false, wantVersion: 2, wantAssignable: false},
		{name: "fresh online vessel", vesselNo: "fresh", initialStatus: "online", heartbeatAge: time.Minute, wantOnline: true, wantVersion: 1, wantAssignable: true},
		{name: "already offline vessel", vesselNo: "offline", initialStatus: "offline", heartbeatAge: time.Hour, wantOnline: false, wantVersion: 1, wantAssignable: false},
	}

	dbPath := t.TempDir() + string(os.PathSeparator) + "restart.db"
	handle, err := store.Open(context.Background(), store.Config{
		SQLitePath:       dbPath,
		HeartbeatTimeout: 5 * time.Minute,
		InspectionPeriod: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("open seed database: %v", err)
	}
	seededAt := time.Now().UTC()
	for i, tc := range cases {
		heartbeatAt := seededAt.Add(-tc.heartbeatAge).Format(time.RFC3339Nano)
		_, err = handle.Exec(context.Background(), `INSERT INTO vessels(
			id, vessel_no, latitude, longitude, position_at, speed_milliknots,
			endurance_seconds, max_operation_millinautical_miles, online_status,
			last_heartbeat_at, active_load, version, created_at, updated_at
		) VALUES(?, ?, 0, 0, ?, 10000, 1000000, 100000, ?, ?, 0, 1, ?, ?)`,
			i+1, tc.vesselNo, heartbeatAt, tc.initialStatus, heartbeatAt, heartbeatAt, heartbeatAt)
		if err != nil {
			t.Fatalf("seed %s: %v", tc.name, err)
		}
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close seed database: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve server address: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release server address: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestModel_RestartRecoveryPrecedesPublicReadsAndScheduling$")
	cmd.Env = append(os.Environ(),
		"TEST_MODEL_RESTART_SERVER=1",
		"TEST_MODEL_RESTART_ADDR="+addr,
		"TEST_MODEL_RESTART_DB="+dbPath,
	)
	serverOutput, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("capture server output: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	client := &http.Client{
		Timeout:   time.Second,
		Transport: &http.Transport{Proxy: nil},
	}
	deadline := time.Now().Add(15 * time.Second)
	var response *http.Response
	for time.Now().Before(deadline) {
		req, requestErr := http.NewRequest(http.MethodGet, "http://"+addr+"/v1/vessels", nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		req.Header.Set("X-Actor-ID", "acceptance-auditor")
		req.Header.Set("X-Role", string(domain.RoleAuditor))
		response, err = client.Do(req)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || response == nil {
		output, _ := io.ReadAll(serverOutput)
		t.Fatalf("server did not become reachable: %v; output: %s", err, output)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read first public response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("first public read status = %d, body = %s", response.StatusCode, body)
	}
	var payload struct {
		Data []fleet.Vessel `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode first public response: %v; body = %s", err, body)
	}
	byNumber := make(map[string]fleet.Vessel, len(payload.Data))
	for _, vessel := range payload.Data {
		byNumber[vessel.VesselNo] = vessel
	}

	sector := drift.Sector{
		Number:       1,
		Priority:     1,
		Centroid:     domain.Position{Latitude: 0, Longitude: 0},
		AreaSquareNM: 1,
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vessel, ok := byNumber[tc.vesselNo]
			if !ok {
				t.Fatalf("vessel %q missing from first public read", tc.vesselNo)
			}
			if vessel.Online != tc.wantOnline {
				t.Errorf("online = %v, want %v", vessel.Online, tc.wantOnline)
			}
			if vessel.Version != tc.wantVersion {
				t.Errorf("version = %d, want %d", vessel.Version, tc.wantVersion)
			}
			plan := fleet.NewScheduler().Generate(1, 1, []drift.Sector{sector}, []fleet.Vessel{vessel}, seededAt)
			assignable := len(plan.Assignments) != 0
			if assignable != tc.wantAssignable {
				t.Errorf("automatic scheduling assignable = %v, want %v (reason %q)", assignable, tc.wantAssignable, plan.UnassignableReasons[sector.Number])
			}
		})
	}
	if len(payload.Data) != len(cases) {
		t.Errorf("GET /v1/vessels returned %d vessels, want %d: %s", len(payload.Data), len(cases), fmt.Sprint(byNumber))
	}
}
