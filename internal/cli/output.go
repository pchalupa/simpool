package cli

import (
	"encoding/json"
	"io"

	"github.com/pchalupa/simpool/internal/errs"
)

// response is the JSON envelope every command emits on stdout.
type response struct {
	OK      bool           `json:"ok"`
	Command string         `json:"command"`
	Data    any            `json:"data,omitempty"`
	Error   *responseError `json:"error,omitempty"`
}

type responseError struct {
	Code    errs.Code      `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func writeResult(out io.Writer, command string, result any) error {
	return writeJSON(out, response{
		OK:      true,
		Command: command,
		Data:    result,
	})
}

// writeError renders a failure on stdout, so a caller reads one stream for
// success and failure alike.
func writeError(out io.Writer, command string, err error) {
	writeJSON(out, response{
		OK:      false,
		Command: command,
		Error: &responseError{
			Code:    errs.CodeOf(err),
			Message: err.Error(),
			Details: errs.DetailsOf(err),
		},
	})
}

func writeJSON(out io.Writer, payload response) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")

	if err := encoder.Encode(payload); err != nil {
		return errs.Wrap(err, errs.CodeInternal, "encoding JSON response")
	}

	return nil
}
