package webhook

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"k8s.io/klog/v2"

	"github.com/drengskapr/external-dns-webhook-ngcloud/internal/ngcloud"
)

// Handler holds the dependencies for all webhook HTTP handlers.
type Handler struct {
	client     *ngcloud.Client
	zoneMap    map[string]string // DNS zone name → ngcloud zone UUID
	domains    []string
	defaultTTL int64
}

func NewHandler(client *ngcloud.Client, zoneMap map[string]string, domains []string) *Handler {
	return &Handler{
		client:     client,
		zoneMap:    zoneMap,
		domains:    domains,
		defaultTTL: client.DefaultTTL(),
	}
}

// Negotiate handles GET / — returns the domain filter for external-dns negotiation.
func (h *Handler) Negotiate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", contentType)
	json.NewEncoder(w).Encode(DomainFilter{Filters: h.domains})
}

// Healthz handles GET /healthz.
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// GetRecords handles GET /records — returns all current DNS records from ngcloud.
func (h *Handler) GetRecords(w http.ResponseWriter, r *http.Request) {
	records, err := h.client.ListRecords()
	if err != nil {
		klog.ErrorS(err, "list records failed")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	endpoints := make([]*Endpoint, 0, len(records))
	for _, rec := range records {
		endpoints = append(endpoints, &Endpoint{
			DNSName:    rec.Name,
			Targets:    []string{rec.Value},
			RecordType: rec.Type,
			RecordTTL:  rec.TTL,
		})
	}

	w.Header().Set("Content-Type", contentType)
	json.NewEncoder(w).Encode(endpoints)
}

// ApplyChanges handles POST /records — applies create/update/delete changes.
func (h *Handler) ApplyChanges(w http.ResponseWriter, r *http.Request) {
	var changes Changes
	if err := json.NewDecoder(r.Body).Decode(&changes); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Deletes first to free up displayNames before creates.
	for _, ep := range changes.Delete {
		klog.V(2).InfoS("deleting record", "name", ep.DNSName, "type", ep.RecordType)
		if err := h.client.DeleteAllByName(ep.DNSName); err != nil {
			klog.ErrorS(err, "delete record failed", "name", ep.DNSName)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Updates: delete all old instances, then create new ones.
	for i, old := range changes.UpdateOld {
		nw := changes.UpdateNew[i]
		klog.V(2).InfoS("updating record", "name", old.DNSName, "type", old.RecordType)
		if err := h.client.DeleteAllByName(old.DNSName); err != nil {
			klog.ErrorS(err, "delete old record failed", "name", old.DNSName)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for idx, target := range nw.Targets {
			rec, err := h.endpointToRecord(nw, target, idx)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if err := h.client.CreateRecord(rec); err != nil {
				klog.ErrorS(err, "create updated record failed", "name", nw.DNSName, "target", target)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}

	// Creates.
	for _, ep := range changes.Create {
		klog.V(2).InfoS("creating record", "name", ep.DNSName, "type", ep.RecordType, "targets", ep.Targets)
		for idx, target := range ep.Targets {
			rec, err := h.endpointToRecord(ep, target, idx)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if err := h.client.CreateRecord(rec); err != nil {
				klog.ErrorS(err, "create record failed", "name", ep.DNSName, "target", target)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// AdjustEndpoints handles POST /adjustendpoints — passthrough, no provider-specific adjustment needed.
func (h *Handler) AdjustEndpoints(w http.ResponseWriter, r *http.Request) {
	var endpoints []*Endpoint
	if err := json.NewDecoder(r.Body).Decode(&endpoints); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", contentType)
	json.NewEncoder(w).Encode(endpoints)
}

// endpointToRecord converts a webhook Endpoint and a single target value into an ngcloud Record.
func (h *Handler) endpointToRecord(ep *Endpoint, target string, idx int) (ngcloud.Record, error) {
	zoneUID, err := h.resolveZoneUID(ep.DNSName)
	if err != nil {
		return ngcloud.Record{}, err
	}
	ttl := ep.RecordTTL
	if ttl == 0 {
		ttl = h.defaultTTL
	}
	return ngcloud.Record{
		ZoneUID:     zoneUID,
		Name:        ep.DNSName,
		Type:        ep.RecordType,
		Value:       target,
		TTL:         ttl,
		TargetIndex: idx,
	}, nil
}

// resolveZoneUID finds the zone UUID for a DNS name by longest-suffix match against the zone map.
func (h *Handler) resolveZoneUID(dnsName string) (string, error) {
	best, bestUID := "", ""
	for zone, uid := range h.zoneMap {
		if strings.HasSuffix(dnsName, zone) && len(zone) > len(best) {
			best = zone
			bestUID = uid
		}
	}
	if bestUID == "" {
		return "", fmt.Errorf("no configured zone matches DNS name %q", dnsName)
	}
	return bestUID, nil
}
