package opensearch

import "encoding/json"

// BulkQueryResponse matches the subset of an OpenSearch bulk response that
// determines whether every finding was accepted.
type BulkQueryResponse struct {
	Took   int                              `json:"took"`
	Errors bool                             `json:"errors"`
	Items  []map[string]BulkQueryItemResult `json:"items"`
}

type BulkQueryItemResult struct {
	Status int             `json:"status"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type QueryResponse struct {
	Schema   []map[string]string `json:"schema"`
	Datarows [][]any             `json:"datarows"`
	Total    int                 `json:"total"`
	Size     int                 `json:"size"`
	Status   int                 `json:"status"`
	Cursor   string              `json:"cursor"`
}

type QueryErrorResponse struct {
	Error  QueryError `json:"error"`
	Status int        `json:"status"`
}

type QueryError struct {
	Reason  string `json:"reason"`
	Details string `json:"details"`
	Type    string `json:"type"`
}

type IndexReq struct {
	Index string `json:"_index"`
	ID    string `json:"_id"`
}

type BulkRequestOp struct {
	Index *IndexReq `json:"index,omitempty"`
}
