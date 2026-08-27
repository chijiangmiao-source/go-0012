package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestModel_ServerRunsConfiguredBackgroundInspection(t *testing.T) {
	const childMarker = "OFFSHORE_BUOY_INSPECTION_TEST_CHILD"
	if os.Getenv(childMarker) == "1" {
		os.Args = []string{
			"offshore-buoy", "server",
			"-addr", os.Getenv("OFFSHORE_BUOY_TEST_ADDR"),
			"-sqlite", os.Getenv("OFFSHORE_BUOY_TEST_DB"),
			"-heartbeat-timeout", "3s",
			"-inspection-period", "50ms",
		}
		main()
		return
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestModel_ServerRunsConfiguredBackgroundInspection$")
	cmd.Env = append(os.Environ(),
		childMarker+"=1",
		"OFFSHORE_BUOY_TEST_ADDR="+addr,
		"OFFSHORE_BUOY_TEST_DB="+filepath.Join(t.TempDir(), "inspection.db"),
	)
	var childOutput bytes.Buffer
	cmd.Stdout = &childOutput
	cmd.Stderr = &childOutput
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var stopOnce sync.Once
	stopChild := func() {
		stopOnce.Do(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
	}
	t.Cleanup(stopChild)

	baseURL := "http://" + addr
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, requestErr := client.Get(baseURL + "/healthz")
		if requestErr == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			stopChild()
			t.Fatalf("server did not become healthy: %s", childOutput.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	doJSON := func(method, path, role string, body any) (int, []byte) {
		t.Helper()
		var requestBody io.Reader
		if body != nil {
			encoded, marshalErr := json.Marshal(body)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			requestBody = bytes.NewReader(encoded)
		}
		req, requestErr := http.NewRequest(method, baseURL+path, requestBody)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Actor-ID", "model-test")
		req.Header.Set("X-Role", role)
		resp, requestErr := client.Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		defer resp.Body.Close()
		responseBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return resp.StatusCode, responseBody
	}

	tests := []struct {
		name          string
		refresh       bool
		wantOnline    bool
		observationBy time.Duration
	}{
		{name: "expired heartbeat becomes offline", wantOnline: false, observationBy: 1500 * time.Millisecond},
		{name: "fresh heartbeat survives later sweeps", refresh: true, wantOnline: true, observationBy: 300 * time.Millisecond},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			vesselNo := fmt.Sprintf("inspection-%d", index)
			status, body := doJSON(http.MethodPost, "/v1/vessels", "operator", map[string]any{
				"vessel_no":                    vesselNo,
				"position":                     map[string]float64{"latitude": 30, "longitude": 122},
				"speed_knots":                  12,
				"endurance_seconds":            36000,
				"max_operation_nautical_miles": 500,
				"last_heartbeat_at":            time.Now().UTC().Add(-time.Minute),
			})
			if status != http.StatusCreated {
				t.Fatalf("create vessel status = %d, body = %s", status, body)
			}
			var created struct {
				Data struct {
					ID int64 `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &created); err != nil {
				t.Fatal(err)
			}
			if test.refresh {
				status, body = doJSON(http.MethodPost, fmt.Sprintf("/v1/vessels/%d/heartbeats", created.Data.ID), "operator", map[string]any{
					"reported_at": time.Now().UTC(),
				})
				if status != http.StatusOK {
					t.Fatalf("heartbeat status = %d, body = %s", status, body)
				}
			}

			observationDeadline := time.Now().Add(test.observationBy)
			for {
				status, body = doJSON(http.MethodGet, "/v1/vessels", "auditor", nil)
				if status != http.StatusOK {
					t.Fatalf("list vessels status = %d, body = %s", status, body)
				}
				var listed struct {
					Data []struct {
						VesselNo string `json:"vessel_no"`
						Online   bool   `json:"online"`
					} `json:"data"`
				}
				if err := json.Unmarshal(body, &listed); err != nil {
					t.Fatal(err)
				}
				found, online := false, false
				for _, vessel := range listed.Data {
					if vessel.VesselNo == vesselNo {
						found, online = true, vessel.Online
						break
					}
				}
				if !found {
					t.Fatalf("created vessel %q missing from response %s", vesselNo, body)
				}
				if test.wantOnline {
					if time.Now().Before(observationDeadline) {
						time.Sleep(20 * time.Millisecond)
						continue
					}
					if !online {
						t.Fatalf("freshly heartbeating vessel %q was marked offline", vesselNo)
					}
					return
				}
				if !online {
					return
				}
				if time.Now().After(observationDeadline) {
					t.Fatalf("expired vessel %q remained online past configured inspection period", vesselNo)
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
}
