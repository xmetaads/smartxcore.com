package email

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
)

func base64Std(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func randHex(n int) string {
	b := make([]byte, n/2+1)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}
