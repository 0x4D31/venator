package bigquery

type Config struct {
	ProjectID      string
	DatasetID      string
	TableID        string
	MaxRows        int
	MaxBytesBilled int64
	MaxResultBytes int64
}
