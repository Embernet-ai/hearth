package emberops

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Sentinels a caller can test with errors.Is. They match by HTTP status or
// Frappe exception type, so a store need not parse messages.
var (
	ErrNotFound     = errors.New("emberops: not found")
	ErrForbidden    = errors.New("emberops: forbidden")
	ErrUnauthorized = errors.New("emberops: not signed in to EmberOps")
	ErrConflict     = errors.New("emberops: modified by someone else")
	ErrInvalid      = errors.New("emberops: rejected as invalid")
	ErrDuplicate    = errors.New("emberops: already exists")
)

// Error is a non-2xx answer from Frappe.
type Error struct {
	Status  int
	Type    string // exc_type, e.g. DoesNotExistError
	Message string
	Path    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("emberops: %s: %d %s: %s", e.Path, e.Status, e.Type, e.Message)
}

// Is maps the answer onto the sentinels.
func (e *Error) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.Status == 404 || e.Type == "DoesNotExistError"
	case ErrForbidden:
		return e.Status == 403 || e.Type == "PermissionError"
	case ErrUnauthorized:
		return e.Status == 401
	case ErrConflict:
		return e.Status == 409 || e.Type == "TimestampMismatchError"
	case ErrDuplicate:
		return e.Type == "DuplicateEntryError" || e.Type == "UniqueValidationError"
	case ErrInvalid:
		return e.Status == 417 || e.Status == 400 || e.Status == 422
	}
	return false
}

// parseError reads Frappe's error envelope: exc_type, exception and
// _server_messages (a JSON list of JSON strings carrying the human message).
func parseError(status int, path string, body []byte) *Error {
	e := &Error{Status: status, Path: path}
	var env struct {
		ExcType        string `json:"exc_type"`
		Exception      string `json:"exception"`
		ServerMessages string `json:"_server_messages"`
		Message        any    `json:"message"`
	}
	if json.Unmarshal(body, &env) != nil {
		e.Message = strings.TrimSpace(string(body))
		return e
	}
	e.Type = env.ExcType
	if e.Type == "" && env.Exception != "" {
		// "frappe.exceptions.DoesNotExistError: Task X not found"
		head, _, _ := strings.Cut(env.Exception, ":")
		if i := strings.LastIndex(head, "."); i >= 0 {
			head = head[i+1:]
		}
		e.Type = strings.TrimSpace(head)
	}
	if env.ServerMessages != "" {
		var list []string
		if json.Unmarshal([]byte(env.ServerMessages), &list) == nil {
			var parts []string
			for _, raw := range list {
				var m struct {
					Message string `json:"message"`
				}
				if json.Unmarshal([]byte(raw), &m) == nil && m.Message != "" {
					parts = append(parts, m.Message)
				}
			}
			e.Message = strings.Join(parts, "; ")
		}
	}
	if e.Message == "" {
		if s, ok := env.Message.(string); ok {
			e.Message = s
		} else {
			e.Message = env.Exception
		}
	}
	return e
}
