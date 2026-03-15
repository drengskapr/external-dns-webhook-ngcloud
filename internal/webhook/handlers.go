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
	client      *ngcloud.Client
	zoneMap     map[string]string // DNS zone name → ngcloud zone UUID
	reverseZone map[string]string // ngcloud zone UUID → DNS zone name (derived from zoneMap)
	domains     []string
	defaultTTL  int64
}

func NewHandler(client *ngcloud.Client, zoneMap map[string]string, domains []string) *Handler {
	rev := make(map[string]string, len(zoneMap))
	for name, uid := range zoneMap {
		rev[uid] = name
	}
	return &Handler{
		client:      client,
		zoneMap:     zoneMap,
		reverseZone: rev,
		domains:     domains,
		defaultTTL:  client.DefaultTTL(),
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
		// Reconstruct FQDN: the API stores the relative name; append the zone.
		dnsName := rec.Name
		if zoneName, ok := h.reverseZone[rec.ZoneUID]; ok {
			dnsName = rec.Name + "." + zoneName
		}
		value := rec.Value
		// Strip the trailing dot added for CNAME storage so external-dns sees the canonical form.
		if rec.Type == "CNAME" {
			value = strings.TrimSuffix(value, ".")
		}
		endpoints = append(endpoints, &Endpoint{
			DNSName:    dnsName,
			Targets:    []string{value},
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
		relativeName, _, err := h.resolveZone(ep.DNSName)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := h.client.DeleteAllByName(relativeName); err != nil {
			klog.ErrorS(err, "delete record failed", "name", ep.DNSName)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Updates: delete all old instances, then create new ones.
	for i, old := range changes.UpdateOld {
		nw := changes.UpdateNew[i]
		klog.V(2).InfoS("updating record", "name", old.DNSName, "type", old.RecordType)
		relativeName, _, err := h.resolveZone(old.DNSName)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := h.client.DeleteAllByName(relativeName); err != nil {
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

// AdjustEndpoints handles POST /adjustendpoints.
// ngcloud enforces uniqueness on recordName per zone, so only one target per
// endpoint is supported. Endpoints with multiple targets are truncated to one.
func (h *Handler) AdjustEndpoints(w http.ResponseWriter, r *http.Request) {
	var endpoints []*Endpoint
	if err := json.NewDecoder(r.Body).Decode(&endpoints); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, ep := range endpoints {
		if len(ep.Targets) > 1 {
			klog.V(2).InfoS("truncating multi-target endpoint to single target (platform limitation)", "name", ep.DNSName, "targets", ep.Targets)
			ep.Targets = ep.Targets[:1]
		}
	}
	w.Header().Set("Content-Type", contentType)
	json.NewEncoder(w).Encode(endpoints)
}

// endpointToRecord converts a webhook Endpoint and a single target value into an ngcloud Record.
// Record.Name is set to the relative name (zone suffix stripped) as required by the deck-api.
func (h *Handler) endpointToRecord(ep *Endpoint, target string, idx int) (ngcloud.Record, error) {
	relativeName, zoneUID, err := h.resolveZone(ep.DNSName)
	if err != nil {
		return ngcloud.Record{}, err
	}
	ttl := ep.RecordTTL
	if ttl == 0 {
		ttl = h.defaultTTL
	}
	value := target
	// CNAME targets must end with a trailing dot for the ngcloud DNS backend.
	if ep.RecordType == "CNAME" && !strings.HasSuffix(value, ".") {
		value += "."
	}
	return ngcloud.Record{
		ZoneUID:     zoneUID,
		Name:        relativeName,
		Type:        ep.RecordType,
		Value:       value,
		TTL:         ttl,
		TargetIndex: idx,
	}, nil
}

// resolveZone finds the zone for a DNS name by longest-suffix match.
// Returns (relativeName, zoneUID, err) where relativeName has the zone suffix stripped.
func (h *Handler) resolveZone(dnsName string) (relativeName, zoneUID string, err error) {
	best, bestUID := "", ""
	for zone, uid := range h.zoneMap {
		if strings.HasSuffix(dnsName, zone) && len(zone) > len(best) {
			best = zone
			bestUID = uid
		}
	}
	if bestUID == "" {
		return "", "", fmt.Errorf("no configured zone matches DNS name %q", dnsName)
	}
	// Strip ".zone" suffix; if dnsName == zone exactly, relative name is "@" convention but
	// external-dns never sends bare zone names, so TrimSuffix("."+zone) is safe.
	rel := strings.TrimSuffix(dnsName, "."+best)
	if rel == dnsName {
		rel = strings.TrimSuffix(dnsName, best)
	}
	return rel, bestUID, nil
}
