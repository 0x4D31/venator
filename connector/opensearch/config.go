package opensearch

type Config struct {
	URL                string
	Username           string
	Password           string
	Index              string
	InsecureSkipVerify bool
	MaxRows            int
	MaxBytes           int64
	SQLFetchSize       int
}
