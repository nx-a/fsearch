package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
)

// maxBodyBytes ограничивает размер тела запроса (8 МБ).
const maxBodyBytes = 8 << 20

// readBody читает тело запроса с ограничением размера.
func readBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBodyBytes))
}

// decodeBody декодирует JSON тела запроса в v.
func decodeBody(r *http.Request, v any) error {
	raw, err := readBody(r)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return errors.New("empty request body")
	}
	return json.Unmarshal(raw, v)
}

// isJSONArray сообщает, начинается ли JSON с массива.
func isJSONArray(raw []byte) bool {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '[':
			return true
		default:
			return false
		}
	}
	return false
}

// parseID разбирает id записи из строки пути.
func parseID(s string) (uint64, error) {
	id, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, errors.New("invalid record id")
	}
	return id, nil
}
