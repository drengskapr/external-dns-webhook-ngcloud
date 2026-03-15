package ngcloud

// Record is an internal representation of a single DNS record managed by ngcloud.
type Record struct {
	InstanceUID string
	ZoneUID     string
	Name        string // fully-qualified DNS name
	Type        string // A, AAAA, CNAME, TXT, …
	Value       string // single target value
	TTL         int64
	TargetIndex int // 0 = primary instance name, 1+ = indexed suffix
}

// deck-api request/response types

type Instance struct {
	InstanceUID      string `json:"instanceUid"`
	DisplayName      string `json:"displayName"`
	IsDeleted        bool   `json:"isDeleted"`
	LastOperation    string `json:"lastOperation"`
	LastOperationUID string `json:"lastOperationUid"`
}

type ListInstancesResponse struct {
	Results []Instance `json:"results"`
	Total   int        `json:"total"`
}

type CreateInstanceRequest struct {
	ServiceID   int    `json:"serviceId"`
	DisplayName string `json:"displayName"`
	Descr       string `json:"descr"`
}

type CFSParamDef struct {
	Label                 string `json:"label"`
	SvcOperationCFSParamID int   `json:"svcOperationCfsParamId"`
}

type SvcOperationDef struct {
	CFSParams []CFSParamDef `json:"cfsParams"`
}

type CFSParamsDefResponse struct {
	SvcOperation SvcOperationDef `json:"svcOperation"`
}

type CreateOperationRequest struct {
	SvcOperationID int    `json:"svcOperationId"`
	InstanceUID    string `json:"instanceUid"`
	Operation      string `json:"operation"`
}

type PushCFSParamRequest struct {
	ParamValue             string `json:"paramValue"`
	InstanceOperationUID   string `json:"instanceOperationUid"`
	SvcOperationCFSParamID int    `json:"svcOperationCfsParamId"`
}

type InstanceOperation struct {
	InstanceOperationUID string `json:"instanceOperationUid"`
	SvcOperationID       int    `json:"svcOperationId"`
	IsSuccessful         bool   `json:"isSuccessful"`
	DtFinish             string `json:"dtFinish"`
	ErrorLog             string `json:"errorLog"`
}

type GetOperationResponse struct {
	InstanceOperation InstanceOperation `json:"instanceOperation"`
}

type CFSParamValue struct {
	SvcOperationCFSParam   string `json:"svcOperationCfsParam"` // internal name, e.g. "zoneUid"
	ParamValue             string `json:"paramValue"`
	SvcOperationCFSParamID int    `json:"svcOperationCfsParamId"`
}

type ListCFSParamValuesResponse struct {
	Results []CFSParamValue `json:"results"`
}
