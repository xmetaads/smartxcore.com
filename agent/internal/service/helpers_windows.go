//go:build windows

package service

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/user"
	"unicode/utf16"
)

func osCreateTemp(pattern string) (*os.File, error) {
	return os.CreateTemp("", pattern)
}

func cleanup(path string) {
	_ = os.Remove(path)
}

// currentUserSID returns the SID of the user running this process.
// schtasks needs the SID for principal/trigger XML elements so the task
// runs only for this user (avoids "for all users" semantics).
func currentUserSID() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	if u.Uid == "" {
		return "", fmt.Errorf("empty SID for current user")
	}
	return u.Uid, nil
}

// utf16WithBOM encodes s as UTF-16LE with byte-order-mark, the only encoding
// that schtasks.exe will accept for /XML input.
func utf16WithBOM(s string) []byte {
	encoded := utf16.Encode([]rune(s))
	buf := make([]byte, 0, 2+len(encoded)*2)
	// BOM
	buf = append(buf, 0xFF, 0xFE)
	tmp := make([]byte, 2)
	for _, r := range encoded {
		binary.LittleEndian.PutUint16(tmp, r)
		buf = append(buf, tmp...)
	}
	return buf
}
