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
	utf8Bytes []byte
	afterCR   bool
}

type state uint8

const (
	stateText state = iota
	stateEscape
	stateCSI
	stateOSC
	stateOSCEscape
)

func (n *Normalizer) Reset() {
	n.state = stateText
	n.utf8Bytes = n.utf8Bytes[:0]
	n.afterCR = false
}

func (n *Normalizer) Feed(input []byte) []byte {
	var output bytes.Buffer
	for _, value := range input {
		switch n.state {
		case stateEscape:
			switch value {
			case '[':
				n.state = stateCSI
			case ']':
				n.state = stateOSC
			default:
				n.state = stateText
			}
			continue
		case stateCSI:
			if value >= 0x40 && value <= 0x7e {
				n.state = stateText
			}
			continue
		case stateOSC:
			if value == 0x07 {
				n.state = stateText
			} else if value == 0x1b {
				n.state = stateOSCEscape
			}
			continue
		case stateOSCEscape:
			if value == '\\' {
				n.state = stateText
			} else if value != 0x1b {
				n.state = stateOSC
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
