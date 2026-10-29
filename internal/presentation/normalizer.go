package presentation

import (
	"bytes"
	"unicode/utf8"
)

// Normalizer incrementally removes terminal control sequences while preserving
// readable UTF-8. Remote PTY text is still untrusted program output; this type
// only makes it suitable for bounded model context, not safe to execute.
type Normalizer struct {
	state     state
	length    int // bytes consumed by the current CSI or control string
	utf8Bytes []byte
	afterCR   bool
}

// Unterminated sequences must not hide later output indefinitely.
const (
	maxCSIBytes    = 256
	maxStringBytes = 1 << 20
)

type state uint8

const (
	stateText state = iota
	stateEscape
	stateEscapeIntermediate
	stateCSI
	stateString // OSC, DCS, SOS, PM, or APC, terminated by BEL or ST
	stateStringEscape
)

func (n *Normalizer) Reset() {
	n.state = stateText
	n.length = 0
	n.utf8Bytes = n.utf8Bytes[:0]
	n.afterCR = false
}

func (n *Normalizer) Feed(input []byte) []byte {
	var output bytes.Buffer
	for _, value := range input {
		switch n.state {
		case stateEscape, stateEscapeIntermediate:
			n.length = 0
			switch {
			case value == '[' && n.state == stateEscape:
				n.state = stateCSI
			case (value == ']' || value == 'P' || value == 'X' || value == '^' || value == '_') && n.state == stateEscape:
				n.state = stateString
			case value >= 0x20 && value <= 0x2f:
				n.state = stateEscapeIntermediate // e.g. ESC ( B charset designation
			case value == 0x1b:
				n.state = stateEscape
			default:
				n.state = stateText // final byte, or a control that cancels the sequence
			}
			continue
		case stateCSI:
			n.length++
			switch {
			case value >= 0x40 && value <= 0x7e:
				n.state = stateText
			case value == 0x1b:
				n.state = stateEscape
			case n.length > maxCSIBytes:
				n.state = stateText
			}
			continue
		case stateString, stateStringEscape:
			n.length++
			switch {
			case value == 0x07 || n.state == stateStringEscape && value == '\\':
				n.state = stateText
			case value == 0x1b:
				n.state = stateStringEscape
			case n.length > maxStringBytes:
				n.state = stateText
			default:
				n.state = stateString
			}
			continue
		}

		if value == 0x1b {
			n.flushInvalidUTF8(&output)
			n.state = stateEscape
			continue
		}
		if value == '\r' {
			n.flushInvalidUTF8(&output)
			output.WriteByte('\n')
			n.afterCR = true
			continue
		}
		if value == '\n' {
			n.flushInvalidUTF8(&output)
			if n.afterCR {
				n.afterCR = false
				continue
			}
			output.WriteByte('\n')
			continue
		}
		n.afterCR = false
		if value == '\t' {
			n.flushInvalidUTF8(&output)
			output.WriteByte(value)
			continue
		}
		if value < 0x20 || value == 0x7f {
			continue
		}
		if value < utf8.RuneSelf && len(n.utf8Bytes) == 0 {
			output.WriteByte(value)
			continue
		}
		n.utf8Bytes = append(n.utf8Bytes, value)
		for len(n.utf8Bytes) > 0 && utf8.FullRune(n.utf8Bytes) {
			r, size := utf8.DecodeRune(n.utf8Bytes)
			if r == utf8.RuneError && size == 1 {
				output.WriteRune(utf8.RuneError)
			} else if r >= 0x80 && r <= 0x9f {
				// Drop C1 controls such as U+009B (CSI).
			} else {
				output.Write(n.utf8Bytes[:size])
			}
			n.utf8Bytes = n.utf8Bytes[size:]
		}
	}
	return output.Bytes()
}

func (n *Normalizer) flushInvalidUTF8(output *bytes.Buffer) {
	if len(n.utf8Bytes) > 0 {
		output.WriteRune(utf8.RuneError)
		n.utf8Bytes = n.utf8Bytes[:0]
	}
}
