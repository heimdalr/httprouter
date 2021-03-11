package httprouter

import (
	"encoding/json"
)

// HTTPError represents an error that occurred while handling a request.
type HTTPError struct {
	Code  int   `json:"code"`
	Error error `json:"error"`
}

func (e HTTPError) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Error string `json:"error"`
	}{Error: e.Error.Error()})
}

