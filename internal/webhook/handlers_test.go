package webhook_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/drengskapr/external-dns-webhook-ngcloud/internal/ngcloud"
	"github.com/drengskapr/external-dns-webhook-ngcloud/internal/webhook"
)

const whContentType = "application/external.dns.webhook+json;version=1"

func newTestServer(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
	token := os.Getenv("NGCLOUD_TOKEN")
	if token == "" {
		t.Skip("NGCLOUD_TOKEN not set")
	}
	zoneName := os.Getenv("TEST_ZONE_NAME")
	if zoneName == "" {
		t.Skip("TEST_ZONE_NAME not set")
	}
	zoneUID := os.Getenv("TEST_ZONE_UID")
	if zoneUID == "" {
		t.Skip("TEST_ZONE_UID not set")
	}

	client, err := ngcloud.New(ngcloud.Config{
		BaseURL:         "https://deck-api.ngcloud.ru/api/v1/index.cfm",
		Token:           token,
		ServiceID:       111,
		OpCreate:        45,
		OpDelete:        46,
		DefaultTTL:      120,
		PollMaxAttempts: 60,
		PollInterval:    5 * time.Second,
	})
	if err != nil {
		t.Fatalf("create ngcloud client: %v", err)
	}

	zoneMap := map[string]string{zoneName: zoneUID}
	h := webhook.NewHandler(client, zoneMap, []string{zoneName})
	srv := webhook.NewServer(h, "0")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, zoneName, zoneUID
}

func do(t *testing.T, ts *httptest.Server, method, path string, body any) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, ts.URL+path, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", whContentType)
	if body != nil {
		req.Header.Set("Content-Type", whContentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func readJSON(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("decode response: %v\nbody: %s", err, b)
	}
}

// TestNegotiate checks that GET / returns a DomainFilter with the configured domain.
func TestNegotiate(t *testing.T) {
	ts, zoneName, _ := newTestServer(t)

	resp := do(t, ts, "GET", "/", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "external.dns.webhook") {
		t.Errorf("unexpected Content-Type: %q", ct)
	}
	var df struct {
		Filters []string `json:"filters"`
	}
	readJSON(t, resp, &df)
	if len(df.Filters) == 0 || df.Filters[0] != zoneName {
		t.Errorf("expected filter %q, got %v", zoneName, df.Filters)
	}
}

// TestHealthz checks that GET /healthz returns 200.
func TestHealthz(t *testing.T) {
	ts, _, _ := newTestServer(t)

	resp := do(t, ts, "GET", "/healthz", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

// TestAdjustEndpoints checks that POST /adjustendpoints is a passthrough.
func TestAdjustEndpoints(t *testing.T) {
	ts, zoneName, _ := newTestServer(t)

	endpoints := []*struct {
		DNSName    string   `json:"dnsName"`
		Targets    []string `json:"targets"`
		RecordType string   `json:"recordType"`
	}{
		{DNSName: "foo." + zoneName, Targets: []string{"1.2.3.4"}, RecordType: "A"},
	}
	resp := do(t, ts, "POST", "/adjustendpoints", endpoints)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
	}
	var out []map[string]any
	readJSON(t, resp, &out)
	if len(out) != 1 {
		t.Errorf("expected 1 endpoint back, got %d", len(out))
	}
}

// TestGetRecords calls GET /records against the live API.
func TestGetRecords(t *testing.T) {
	ts, _, _ := newTestServer(t)

	resp := do(t, ts, "GET", "/records", nil)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, b)
	}
	var endpoints []map[string]any
	readJSON(t, resp, &endpoints)
	t.Logf("GET /records returned %d endpoints", len(endpoints))
}

// TestApplyChangesCreateDelete posts a create change, then a delete change,
// and verifies the record appears and disappears from GET /records.
func TestApplyChangesCreateDelete(t *testing.T) {
	ts, zoneName, _ := newTestServer(t)

	dnsName := fmt.Sprintf("webhook-e2e.%s", zoneName)

	changes := map[string]any{
		"create": []map[string]any{
			{"dnsName": dnsName, "targets": []string{"9.8.7.6"}, "recordType": "A", "recordTTL": 120},
		},
		"updateOld": []map[string]any{},
		"updateNew": []map[string]any{},
		"delete":    []map[string]any{},
	}

	t.Log("POST /records — create")
	resp := do(t, ts, "POST", "/records", changes)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("create: expected 204, got %d", resp.StatusCode)
	}

	t.Log("GET /records — verify record present")
	resp = do(t, ts, "GET", "/records", nil)
	var endpoints []map[string]any
	readJSON(t, resp, &endpoints)
	found := false
	for _, ep := range endpoints {
		if ep["dnsName"] == dnsName {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("created record %q not found in GET /records", dnsName)
	}

	deleteChanges := map[string]any{
		"create":    []map[string]any{},
		"updateOld": []map[string]any{},
		"updateNew": []map[string]any{},
		"delete": []map[string]any{
			{"dnsName": dnsName, "targets": []string{"9.8.7.6"}, "recordType": "A"},
		},
	}

	t.Log("POST /records — delete")
	resp = do(t, ts, "POST", "/records", deleteChanges)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d", resp.StatusCode)
	}
}

// TestApplyChangesUpdate creates a record, updates it to a new IP, then deletes it.
func TestApplyChangesUpdate(t *testing.T) {
	ts, zoneName, _ := newTestServer(t)

	dnsName := fmt.Sprintf("webhook-update.%s", zoneName)

	create := map[string]any{
		"create": []map[string]any{
			{"dnsName": dnsName, "targets": []string{"1.1.1.1"}, "recordType": "A", "recordTTL": 120},
		},
		"updateOld": []map[string]any{},
		"updateNew": []map[string]any{},
		"delete":    []map[string]any{},
	}
	t.Log("creating record")
	resp := do(t, ts, "POST", "/records", create)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("create: expected 204, got %d", resp.StatusCode)
	}

	update := map[string]any{
		"create": []map[string]any{},
		"updateOld": []map[string]any{
			{"dnsName": dnsName, "targets": []string{"1.1.1.1"}, "recordType": "A"},
		},
		"updateNew": []map[string]any{
			{"dnsName": dnsName, "targets": []string{"2.2.2.2"}, "recordType": "A", "recordTTL": 120},
		},
		"delete": []map[string]any{},
	}
	t.Log("updating record")
	resp = do(t, ts, "POST", "/records", update)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("update: expected 204, got %d", resp.StatusCode)
	}

	del := map[string]any{
		"create":    []map[string]any{},
		"updateOld": []map[string]any{},
		"updateNew": []map[string]any{},
		"delete": []map[string]any{
			{"dnsName": dnsName, "targets": []string{"2.2.2.2"}, "recordType": "A"},
		},
	}
	t.Log("deleting record")
	resp = do(t, ts, "POST", "/records", del)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d", resp.StatusCode)
	}
}

// TestApplyChangesCNAME creates a CNAME record (without trailing dot, as external-dns sends it),
// verifies it appears in GET /records without trailing dot, then deletes it.
func TestApplyChangesCNAME(t *testing.T) {
	ts, zoneName, _ := newTestServer(t)

	dnsName := fmt.Sprintf("webhook-cname-e2e.%s", zoneName)
	cnameTarget := fmt.Sprintf("webhook-e2e.%s", zoneName) // no trailing dot

	create := map[string]any{
		"create": []map[string]any{
			{"dnsName": dnsName, "targets": []string{cnameTarget}, "recordType": "CNAME", "recordTTL": 120},
		},
		"updateOld": []map[string]any{},
		"updateNew": []map[string]any{},
		"delete":    []map[string]any{},
	}

	t.Log("POST /records — create CNAME")
	resp := do(t, ts, "POST", "/records", create)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("create: expected 204, got %d", resp.StatusCode)
	}

	t.Log("GET /records — verify CNAME present without trailing dot")
	resp = do(t, ts, "GET", "/records", nil)
	var endpoints []map[string]any
	readJSON(t, resp, &endpoints)
	found := false
	for _, ep := range endpoints {
		if ep["dnsName"] == dnsName {
			targets, _ := ep["targets"].([]any)
			if len(targets) > 0 && targets[0] == cnameTarget {
				found = true
			} else {
				t.Errorf("CNAME target: want %q, got %v", cnameTarget, targets)
			}
			break
		}
	}
	if !found {
		t.Errorf("CNAME record %q not found in GET /records", dnsName)
	}

	del := map[string]any{
		"create":    []map[string]any{},
		"updateOld": []map[string]any{},
		"updateNew": []map[string]any{},
		"delete": []map[string]any{
			{"dnsName": dnsName, "targets": []string{cnameTarget}, "recordType": "CNAME"},
		},
	}

	t.Log("POST /records — delete CNAME")
	resp = do(t, ts, "POST", "/records", del)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d", resp.StatusCode)
	}
}
