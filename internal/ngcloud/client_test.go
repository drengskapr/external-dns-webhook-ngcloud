package ngcloud_test

import (
	"os"
	"testing"
	"time"

	"github.com/drengskapr/external-dns-webhook-ngcloud/internal/ngcloud"
)

func newTestClient(t *testing.T) *ngcloud.Client {
	t.Helper()
	token := os.Getenv("NGCLOUD_TOKEN")
	if token == "" {
		t.Skip("NGCLOUD_TOKEN not set")
	}
	if os.Getenv("TEST_ZONE_UID") == "" {
		t.Skip("TEST_ZONE_UID not set")
	}
	c, err := ngcloud.New(ngcloud.Config{
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
		t.Fatalf("create client: %v", err)
	}
	return c
}

func testZoneUID(t *testing.T) string {
	t.Helper()
	v := os.Getenv("TEST_ZONE_UID")
	if v == "" {
		t.Skip("TEST_ZONE_UID not set")
	}
	return v
}

func testZoneName(t *testing.T) string {
	t.Helper()
	v := os.Getenv("TEST_ZONE_NAME")
	if v == "" {
		t.Skip("TEST_ZONE_NAME not set")
	}
	return v
}

// TestCreateDeleteRecord creates an A record and then deletes it.
// This mirrors the most critical path: the confirmed 6-step create flow.
// Record.Name must be the relative name within the zone (no zone suffix) —
// the deck-api appends the zone automatically.
func TestCreateDeleteRecord(t *testing.T) {
	c := newTestClient(t)
	zoneUID := testZoneUID(t)
	_ = testZoneName(t)

	rec := ngcloud.Record{
		ZoneUID:     zoneUID,
		Name:        "webhook-test",
		Type:        "A",
		Value:       "1.2.3.4",
		TTL:         120,
		TargetIndex: 0,
	}

	t.Log("creating record")
	if err := c.CreateRecord(rec); err != nil {
		t.Fatalf("CreateRecord: %v", err)
	}
	deleted := false
	t.Cleanup(func() {
		if !deleted {
			if err := c.DeleteAllByName(rec.Name); err != nil {
				t.Logf("cleanup: DeleteAllByName(%q): %v", rec.Name, err)
			}
		}
	})
	t.Log("record created")

	t.Log("deleting record")
	if err := c.DeleteAllByName(rec.Name); err != nil {
		t.Fatalf("DeleteAllByName: %v", err)
	}
	deleted = true
	t.Log("record deleted")
}

// TestCreateDeleteTXTRecord creates a TXT record and then deletes it.
func TestCreateDeleteTXTRecord(t *testing.T) {
	c := newTestClient(t)
	zoneUID := testZoneUID(t)

	rec := ngcloud.Record{
		ZoneUID:     zoneUID,
		Name:        "webhook-txt-test",
		Type:        "TXT",
		Value:       "v=spf1 include:example.com ~all",
		TTL:         120,
		TargetIndex: 0,
	}

	t.Log("creating TXT record")
	if err := c.CreateRecord(rec); err != nil {
		t.Fatalf("CreateRecord: %v", err)
	}
	deleted := false
	t.Cleanup(func() {
		if !deleted {
			if err := c.DeleteAllByName(rec.Name); err != nil {
				t.Logf("cleanup: DeleteAllByName(%q): %v", rec.Name, err)
			}
		}
	})
	t.Log("TXT record created")

	t.Log("deleting TXT record")
	if err := c.DeleteAllByName(rec.Name); err != nil {
		t.Fatalf("DeleteAllByName: %v", err)
	}
	deleted = true
	t.Log("TXT record deleted")
}

// TestCreateDeleteCNAMERecord creates a CNAME record and then deletes it.
func TestCreateDeleteCNAMERecord(t *testing.T) {
	c := newTestClient(t)
	zoneUID := testZoneUID(t)
	zoneName := testZoneName(t)

	rec := ngcloud.Record{
		ZoneUID:     zoneUID,
		Name:        "webhook-cname-test",
		Type:        "CNAME",
		Value:       "webhook-test." + zoneName + ".", // trailing dot required by DNS backend
		TTL:         120,
		TargetIndex: 0,
	}

	t.Log("creating CNAME record")
	if err := c.CreateRecord(rec); err != nil {
		t.Fatalf("CreateRecord: %v", err)
	}
	deleted := false
	t.Cleanup(func() {
		if !deleted {
			if err := c.DeleteAllByName(rec.Name); err != nil {
				t.Logf("cleanup: DeleteAllByName(%q): %v", rec.Name, err)
			}
		}
	})
	t.Log("CNAME record created")

	t.Log("deleting CNAME record")
	if err := c.DeleteAllByName(rec.Name); err != nil {
		t.Fatalf("DeleteAllByName: %v", err)
	}
	deleted = true
	t.Log("CNAME record deleted")
}

// TestListRecords lists all records. Exercises the two unconfirmed API endpoints
// (GET /instanceOperations?instanceUid=... and GET /instanceOperationCfsParams?instanceOperationUid=...).
func TestListRecords(t *testing.T) {
	c := newTestClient(t)

	records, err := c.ListRecords()
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	t.Logf("found %d records", len(records))
	for _, r := range records {
		t.Logf("  name=%s type=%s value=%s ttl=%d", r.Name, r.Type, r.Value, r.TTL)
	}
}

// TestCreateListDelete creates a record, lists records to confirm it appears,
// then deletes it.
func TestCreateListDelete(t *testing.T) {
	c := newTestClient(t)
	zoneUID := testZoneUID(t)
	_ = testZoneName(t)

	rec := ngcloud.Record{
		ZoneUID:     zoneUID,
		Name:        "webhook-list-test",
		Type:        "A",
		Value:       "5.6.7.8",
		TTL:         120,
		TargetIndex: 0,
	}

	t.Log("creating record")
	if err := c.CreateRecord(rec); err != nil {
		t.Fatalf("CreateRecord: %v", err)
	}
	deleted := false
	t.Cleanup(func() {
		if !deleted {
			if err := c.DeleteAllByName(rec.Name); err != nil {
				t.Logf("cleanup: DeleteAllByName(%q): %v", rec.Name, err)
			}
		}
	})

	t.Log("listing records")
	records, err := c.ListRecords()
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	found := false
	for _, r := range records {
		if r.Name == rec.Name && r.Value == rec.Value { // rec.Name is the relative name
			found = true
			break
		}
	}
	if !found {
		t.Errorf("created record not found in ListRecords output")
	}

	t.Log("deleting record")
	if err := c.DeleteAllByName(rec.Name); err != nil {
		t.Fatalf("DeleteAllByName: %v", err)
	}
	deleted = true
}

