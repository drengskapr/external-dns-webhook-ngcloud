package webhook

// Endpoint represents a DNS record as defined by the external-dns webhook API spec.
type Endpoint struct {
	DNSName    string   `json:"dnsName"`
	Targets    []string `json:"targets"`
	RecordType string   `json:"recordType"`
	RecordTTL  int64    `json:"recordTTL,omitempty"`
}

// Changes holds the desired DNS record changes sent by external-dns.
type Changes struct {
	Create    []*Endpoint `json:"create"`
	UpdateOld []*Endpoint `json:"updateOld"`
	UpdateNew []*Endpoint `json:"updateNew"`
	Delete    []*Endpoint `json:"delete"`
}

// DomainFilter is returned by GET / for external-dns negotiation.
type DomainFilter struct {
	Filters []string `json:"filters"`
}
