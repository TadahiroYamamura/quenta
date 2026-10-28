package task

import (
	"encoding/hex"
	"io"
)

// NewID は、16 進の小文字 8 桁のランダムな ID を作る
func NewID(random io.Reader) (string, error) {
	var b [4]byte
	if _, err := io.ReadFull(random, b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
