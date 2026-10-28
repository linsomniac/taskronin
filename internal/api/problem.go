package api

import "encoding/json"

// Problem is the API's RFC 9457 error representation.
type Problem struct {
	Type       string          `json:"type"`
	Title      string          `json:"title"`
	Status     int             `json:"status"`
	Detail     string          `json:"detail"`
	Code       string          `json:"code"`
	RequestID  string          `json:"request_id"`
	Errors     []FieldError    `json:"errors,omitempty"`
	Conflicts  []Conflict      `json:"conflicts,omitempty"`
	Current    json.RawMessage `json:"current,omitempty"`
	RetryAfter int             `json:"retry_after,omitempty"`
}

// Error returns the problem code and its safe detail.
func (p *Problem) Error() string { return p.Code + ": " + p.Detail }

// FieldError describes a validation failure for a request field.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Conflict identifies an operation and entity that prevent a revert.
type Conflict struct {
	OpID     string `json:"op_id"`
	Kind     string `json:"kind"`
	Actor    string `json:"actor"`
	At       Time   `json:"at"`
	Entity   string `json:"entity"`
	EntityID int64  `json:"entity_id"`
}
