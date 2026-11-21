// Package core is a Go translation of dsh-superagent-prototype src/core: the domain model, ports and Engine of a
// personal agent, independent of any Source, Policy implementation or Executor.
package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Key returns a stable content-derived identifier; equal inputs always produce the same id. It hashes the same bytes as
// SA's key(): sha256 of JSON.stringify(values), first 32 hex characters.
func Key(values ...string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(jsString(v))
	}
	b.WriteByte(']')
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:32]
}

// jsString quotes s as JSON.stringify does.
func jsString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString("\\\"")
		case '\\':
			b.WriteString("\\\\")
		case '\b':
			b.WriteString("\\b")
		case '\f':
			b.WriteString("\\f")
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\t':
			b.WriteString("\\t")
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, "\\u%04x", r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
