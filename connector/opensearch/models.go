package opensearch

import "encoding/json"

type QueryRequest struct {
	Query     string `json:"query,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	FetchSize *int   `json:"fetch_size,omitempty"`
}

type QueryColumn struct {
	Name  string `json:"name"`
	Alias string `json:"alias"`
	Type  string `json:"type"`
}

// BulkQueryResponse matches the subset of an OpenSearch bulk response that
// determines whether every finding was accepted.
type BulkQueryResponse struct {
	Errors bool                             `json:"errors"`
	Items  []map[string]BulkQueryItemResult `json:"items"`
}

type BulkQueryItemResult struct {
	Status int             `json:"status"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type QueryResponse struct {
	Schema   []QueryColumn `json:"schema"`
	Datarows [][]any       `json:"datarows"`
	Total    *int          `json:"total,omitempty"`
	Size     *int          `json:"size,omitempty"`
	Status   int           `json:"status"`
	Cursor   string        `json:"cursor"`
}

type QueryErrorResponse struct {
	Error QueryError `json:"error"`
}

type QueryError struct {
	Reason  string `json:"reason"`
	Details string `json:"details"`
}

type IndexReq struct {
	Index string `json:"_index"`
	ID    string `json:"_id"`
}

type BulkRequestOp struct {
	Index *IndexReq `json:"index,omitempty"`
}
