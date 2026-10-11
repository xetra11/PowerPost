package platform

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// APIError is a non-2xx (or error-carrying) platform response.
type APIError struct {
	Status  int
	Message string
	Body    string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body)
}

// DoJSON sends req and decodes a JSON response into out (may be nil).
// errMsg extracts a platform-specific error message from the body; it may return "".
func DoJSON(c *http.Client, req *http.Request, out any, errMsg func([]byte) string) error {
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := ""
		if errMsg != nil {
			msg = errMsg(body)
		}
		return &APIError{Status: resp.StatusCode, Message: msg, Body: truncate(string(body), 500)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("unexpected response (%s): %s", err, truncate(string(body), 300))
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
