package internal

import (
	"bytes"
	"encoding/base64"
)

// Checks if it's printable ASCII, otherwise returns base64-encoded string of the input, stripping trailing newlines and carriage returns
func PlainOrBase64(s []byte) (value string) {
	// trim trailing newlines and carriage returns
	s = bytes.TrimRight(s, "\r\n")
	for _, r := range s {
		if r < 32 || r > 126 { // Noisy characters
			return base64.StdEncoding.EncodeToString(s)
		}
	}

	return string(s)
}
