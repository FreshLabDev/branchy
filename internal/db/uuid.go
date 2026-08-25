// SPDX-License-Identifier: Apache-2.0
package db

import (
	"crypto/rand"
	"fmt"
	"strings"
)

// NewUUID returns a random RFC 4122 version 4 UUID. Notification jobs generate
// the id in Go so a PR More callback can embed it before INSERT.
func NewUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func CompactUUID(id string) string {
	return strings.ToLower(strings.ReplaceAll(id, "-", ""))
}

func ExpandCompactUUID(compact string) (string, bool) {
	if len(compact) != 32 {
		return "", false
	}
	var b strings.Builder
	b.Grow(36)
	for i, r := range compact {
		if r >= 'A' && r <= 'F' {
			r += 'a' - 'A'
		}
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return "", false
		}
		if i == 8 || i == 12 || i == 16 || i == 20 {
			b.WriteByte('-')
		}
		b.WriteRune(r)
	}
	return b.String(), true
}
