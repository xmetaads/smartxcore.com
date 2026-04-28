package services

import "crypto/rand"

// cryptoRandRead is a thin wrapper kept in its own file so tests can
// override randomness without depending on the package layout of go's
// standard library directly.
func cryptoRandRead(b []byte) (int, error) {
	return rand.Read(b)
}
