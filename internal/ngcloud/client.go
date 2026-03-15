package ngcloud

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

// CFS parameter labels (Russian strings) used for the create operation's fetchCFSParamDefs.
const (
	cfsLabelZoneUID    = "UUID Зоны"
	cfsLabelRecordType = "Тип DNS-записи"
	cfsLabelName       = "Имя записи"
	cfsLabelValue      = "Значение записи"
	cfsLabelTTL        = "TTL записи (в секундах)"
)

// CFS parameter internal names returned by GET /instanceOperationCfsParams.
const (
	cfsParamZoneUID    = "zoneUid"
	cfsParamRecordType = "recordType"
	cfsParamName       = "recordName"
	cfsParamValue      = "recordInput"
	cfsParamTTL        = "recordTTL"
)

var operationUIDRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// Config holds all ngcloud client configuration.
type Config struct {
	BaseURL         string
	Token           string
	ServiceID       int
	OpCreate        int
	OpDelete        int
	DefaultTTL      int64
	PollMaxAttempts int
	PollInterval    time.Duration
}

// Client interacts with the ngcloud deck-api.
type Client struct {
	cfg       Config
	http      *http.Client
	cfsCreate map[string]int // label → svcOperationCfsParamId, cached at startup
}

// New creates a Client and pre-fetches CFS param definitions for the create operation.
func New(cfg Config) (*Client, error) {
	c := &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 30 * time.Second},
	}

	var err error
	c.cfsCreate, err = c.fetchCFSParamDefs(cfg.OpCreate)
	if err != nil {
		return nil, fmt.Errorf("fetch CFS params for create op %d: %w", cfg.OpCreate, err)
	}
	klog.V(2).InfoS("CFS params cached", "opCreate", cfg.OpCreate, "count", len(c.cfsCreate))

	return c, nil
}

func (c *Client) DefaultTTL() int64 {
	return c.cfg.DefaultTTL
}

// ListRecords returns all DNS records managed by this provider.
// It lists all instances and for each active (lastOperation=="create") instance
// fetches the CFS param values from the create operation to reconstruct the record.
func (c *Client) ListRecords() ([]Record, error) {
	instances, err := c.listAllInstances()
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}

	var records []Record
	for _, inst := range instances {
		if inst.LastOperation != "create" || inst.LastOperationUID == "" {
			klog.V(4).InfoS("skip instance: not an active create", "instanceUID", inst.InstanceUID, "lastOp", inst.LastOperation)
			continue
		}

		values, err := c.getCFSParamValues(inst.LastOperationUID)
		if err != nil {
			klog.V(4).InfoS("skip instance: could not read CFS values", "instanceUID", inst.InstanceUID, "err", err)
			continue
		}

		r := Record{
			InstanceUID: inst.InstanceUID,
			ZoneUID:     values[cfsParamZoneUID],
			Type:        values[cfsParamRecordType],
			Name:        values[cfsParamName],
			Value:       values[cfsParamValue],
		}
		if ttlStr := values[cfsParamTTL]; ttlStr != "" {
			r.TTL, _ = strconv.ParseInt(ttlStr, 10, 64)
		}
		if r.Name == "" || r.Type == "" || r.Value == "" {
			continue
		}
		records = append(records, r)
	}
	return records, nil
}

// CreateRecord creates a single DNS record (one instance per target value).
func (c *Client) CreateRecord(r Record) error {
	displayName := instanceDisplayName(r.Name, r.TargetIndex)

	_, _, err := c.post("instances", CreateInstanceRequest{
		ServiceID:   c.cfg.ServiceID,
		DisplayName: displayName,
		Descr:       "",
	})
	if err != nil {
		if !strings.Contains(err.Error(), "not unique") {
			return fmt.Errorf("create instance: %w", err)
		}
		// Deleted instances permanently hold their display names in the uniqueness index.
		// Fall back to a randomised display name; deletion will find instances by recordName CFS param.
		displayName = fmt.Sprintf("%s-%x", displayName, rand.Int31())
		klog.V(2).InfoS("display name taken, using unique fallback", "displayName", displayName)
		if _, _, err2 := c.post("instances", CreateInstanceRequest{
			ServiceID:   c.cfg.ServiceID,
			DisplayName: displayName,
			Descr:       "",
		}); err2 != nil {
			return fmt.Errorf("create instance (fallback): %w", err2)
		}
	}

	instanceUID, err := c.findInstanceUID(displayName)
	if err != nil {
		return fmt.Errorf("find instance after create: %w", err)
	}
	klog.V(2).InfoS("instance created", "instanceUID", instanceUID, "displayName", displayName)

	opUID, err := c.createOperation(instanceUID, c.cfg.OpCreate, "create")
	if err != nil {
		return fmt.Errorf("create operation: %w", err)
	}
	klog.V(2).InfoS("operation created", "operationUID", opUID)

	ttl := r.TTL
	if ttl == 0 {
		ttl = c.cfg.DefaultTTL
	}
	params := map[string]string{
		cfsLabelZoneUID:    r.ZoneUID,
		cfsLabelRecordType: r.Type,
		cfsLabelName:       r.Name,
		cfsLabelValue:      r.Value,
		cfsLabelTTL:        strconv.FormatInt(ttl, 10),
	}
	for label, value := range params {
		id, ok := c.cfsCreate[label]
		if !ok {
			return fmt.Errorf("unknown CFS label %q (not returned by API)", label)
		}
		if err := c.pushCFSParam(opUID, id, value); err != nil {
			return fmt.Errorf("push CFS param %q: %w", label, err)
		}
	}
	klog.V(2).InfoS("CFS params pushed", "operationUID", opUID)

	if err := c.runOperation(opUID); err != nil {
		return fmt.Errorf("run operation: %w", err)
	}
	return c.pollOperation(opUID)
}

// DeleteAllByName deletes all ngcloud instances whose recordName CFS param matches name.
// Matching by CFS param (not display name) makes deletion robust to display-name reuse constraints.
func (c *Client) DeleteAllByName(name string) error {
	records, err := c.ListRecords()
	if err != nil {
		return fmt.Errorf("list records for delete: %w", err)
	}
	for _, rec := range records {
		if rec.Name != name {
			continue
		}
		klog.V(2).InfoS("deleting instance", "instanceUID", rec.InstanceUID, "name", name)
		if err := c.deleteInstance(rec.InstanceUID); err != nil {
			return fmt.Errorf("delete instance %s: %w", rec.InstanceUID, err)
		}
	}
	return nil
}

// --- internal helpers ---

func instanceDisplayName(name string, idx int) string {
	if idx == 0 {
		return "dnsrecord-" + name
	}
	return fmt.Sprintf("dnsrecord-%s-%d", name, idx)
}

func (c *Client) fetchCFSParamDefs(opID int) (map[string]int, error) {
	path := fmt.Sprintf("instanceOperations/default/%d", opID)
	q := url.Values{"fields": {"operation,svcOperationId,cfsParams"}}
	body, err := c.get(path, q)
	if err != nil {
		return nil, err
	}
	var resp CFSParamsDefResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse CFS params response: %w", err)
	}
	m := make(map[string]int, len(resp.SvcOperation.CFSParams))
	for _, p := range resp.SvcOperation.CFSParams {
		m[p.Label] = p.SvcOperationCFSParamID
	}
	return m, nil
}

// getCFSParamValues returns CFS param label→value pairs for a completed operation.
func (c *Client) getCFSParamValues(opUID string) (map[string]string, error) {
	q := url.Values{"instanceOperationUid": {opUID}}
	body, err := c.get("instanceOperationCfsParams", q)
	if err != nil {
		return nil, err
	}
	var resp ListCFSParamValuesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	m := make(map[string]string, len(resp.Results))
	for _, v := range resp.Results {
		m[v.SvcOperationCFSParam] = v.ParamValue
	}
	return m, nil
}

func (c *Client) listAllInstances() ([]Instance, error) {
	var all []Instance
	var totalReceived int
	for page := 1; ; page++ {
		q := url.Values{
			"serviceId": {strconv.Itoa(c.cfg.ServiceID)},
			"page":      {strconv.Itoa(page)},
			"pageSize":  {"100"},
		}
		body, err := c.get("instances", q)
		if err != nil {
			return nil, err
		}
		var resp ListInstancesResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, err
		}
		for _, inst := range resp.Results {
			if !inst.IsDeleted {
				all = append(all, inst)
			}
		}
		totalReceived += len(resp.Results)
		if len(resp.Results) == 0 || totalReceived >= resp.Total {
			break
		}
	}
	return all, nil
}

func (c *Client) findInstanceUID(displayName string) (string, error) {
	q := url.Values{
		"fields":    {"instanceUid,displayName"},
		"serviceId": {strconv.Itoa(c.cfg.ServiceID)},
		"page":      {"1"},
		"pageSize":  {"100"},
	}
	body, err := c.get("instances", q)
	if err != nil {
		return "", err
	}
	var resp ListInstancesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	for _, inst := range resp.Results {
		if inst.DisplayName == displayName {
			return inst.InstanceUID, nil
		}
	}
	return "", fmt.Errorf("instance with displayName %q not found", displayName)
}


func (c *Client) createOperation(instanceUID string, svcOpID int, opName string) (string, error) {
	headers, _, err := c.post("instanceOperations", CreateOperationRequest{
		SvcOperationID: svcOpID,
		InstanceUID:    instanceUID,
		Operation:      opName,
	})
	if err != nil {
		return "", err
	}
	location := headers.Get("Location")
	uid := operationUIDRe.FindString(location)
	if uid == "" {
		return "", fmt.Errorf("operation UID not found in Location header: %q", location)
	}
	return uid, nil
}

func (c *Client) pushCFSParam(opUID string, cfsParamID int, value string) error {
	_, _, err := c.post("instanceOperationCfsParams", PushCFSParamRequest{
		ParamValue:             value,
		InstanceOperationUID:   opUID,
		SvcOperationCFSParamID: cfsParamID,
	})
	return err
}

func (c *Client) runOperation(opUID string) error {
	_, _, err := c.post(fmt.Sprintf("instanceOperations/%s/run", opUID), nil)
	if err != nil {
		// The deck-api /run endpoint sometimes returns HTTP 500 but still queues the
		// job successfully. Log the error and proceed to polling — the poll will
		// determine the actual outcome.
		klog.V(2).InfoS("run endpoint returned error, proceeding to poll", "operationUID", opUID, "err", err)
	}
	return nil
}

func (c *Client) deleteInstance(instanceUID string) error {
	opUID, err := c.createOperation(instanceUID, c.cfg.OpDelete, "delete")
	if err != nil {
		return err
	}
	if err := c.runOperation(opUID); err != nil {
		return err
	}
	return c.pollOperation(opUID)
}

func (c *Client) pollOperation(opUID string) error {
	for attempt := range c.cfg.PollMaxAttempts {
		body, err := c.get(fmt.Sprintf("instanceOperations/%s", opUID), nil)
		if err != nil {
			return fmt.Errorf("poll: %w", err)
		}
		var resp GetOperationResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return fmt.Errorf("poll parse: %w", err)
		}
		op := resp.InstanceOperation
		if op.DtFinish != "" && op.DtFinish != "null" {
			if op.IsSuccessful {
				klog.V(2).InfoS("operation completed", "operationUID", opUID, "dtFinish", op.DtFinish)
				return nil
			}
			return fmt.Errorf("operation %s failed: %s", opUID, op.ErrorLog)
		}
		klog.V(4).InfoS("operation in progress", "operationUID", opUID, "attempt", attempt+1, "max", c.cfg.PollMaxAttempts)
		time.Sleep(c.cfg.PollInterval)
	}
	return fmt.Errorf("operation %s timed out after %d attempts", opUID, c.cfg.PollMaxAttempts)
}

// --- HTTP helpers ---

func (c *Client) url(path string) string {
	return strings.TrimRight(c.cfg.BaseURL, "/") + "/" + path
}

func (c *Client) get(path string, query url.Values) ([]byte, error) {
	u := c.url(path)
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: HTTP %d: %s", path, resp.StatusCode, body)
	}
	return body, nil
}

func (c *Client) post(path string, payload any) (http.Header, []byte, error) {
	var bodyReader io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, err
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(http.MethodPost, c.url(path), bodyReader)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("POST %s: HTTP %d: %s", path, resp.StatusCode, respBody)
	}
	return resp.Header, respBody, nil
}
