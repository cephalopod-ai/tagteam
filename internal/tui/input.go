package tui

import (
	"bytes"
	"unicode/utf8"
)

type keyKind int

const (
	keyRune keyKind = iota
	keyEnter
	keyBackspace
	keyTab
	keyEsc
	keyUp
	keyDown
	keyLeft
	keyRight
	keyPageUp
	keyPageDown
	keyHome
	keyEnd
)

type keyEvent struct {
	Kind keyKind
	Rune rune
}

type inputDecoder struct {
	pending []byte
}

func (d *inputDecoder) Feed(chunk []byte) []keyEvent {
	d.pending = append(d.pending, chunk...)
	events, pending := decodeKeyEventPrefix(d.pending, false)
	d.pending = append(d.pending[:0], pending...)
	return events
}

func (d *inputDecoder) Flush() []keyEvent {
	events, pending := decodeKeyEventPrefix(d.pending, true)
	d.pending = append(d.pending[:0], pending...)
	return events
}

func (d *inputDecoder) HasPending() bool {
	return len(d.pending) > 0
}

// decodeKeyEvents remains useful for deterministic unit tests where the input
// buffer is known to be complete.
func decodeKeyEvents(chunk []byte) []keyEvent {
	decoder := inputDecoder{}
	events := decoder.Feed(chunk)
	return append(events, decoder.Flush()...)
}

func decodeKeyEventPrefix(chunk []byte, flush bool) ([]keyEvent, []byte) {
	events := make([]keyEvent, 0, len(chunk))
	for len(chunk) > 0 {
		if chunk[0] == 27 {
			matched, incomplete, kind, size := decodeEscapeSequence(chunk)
			if incomplete && !flush {
				return events, chunk
			}
			if matched {
				events = append(events, keyEvent{Kind: kind})
				chunk = chunk[size:]
				continue
			}
			events = append(events, keyEvent{Kind: keyEsc})
			chunk = chunk[1:]
			continue
		}

		b := chunk[0]
		switch b {
		case 3:
			events = append(events, keyEvent{Kind: keyEsc})
			chunk = chunk[1:]
		case 9:
			events = append(events, keyEvent{Kind: keyTab})
			chunk = chunk[1:]
		case 10, 13:
			events = append(events, keyEvent{Kind: keyEnter})
			chunk = chunk[1:]
		case 127, 8:
			events = append(events, keyEvent{Kind: keyBackspace})
			chunk = chunk[1:]
		default:
			if b < 32 {
				chunk = chunk[1:]
				continue
			}
			r, size := utf8.DecodeRune(chunk)
			if r == utf8.RuneError && size == 1 {
				if !flush && !utf8.FullRune(chunk) {
					return events, chunk
				}
				r = rune(b)
			}
			events = append(events, keyEvent{Kind: keyRune, Rune: r})
			chunk = chunk[size:]
		}
	}
	return events, nil
}

func decodeEscapeSequence(chunk []byte) (matched, incomplete bool, kind keyKind, size int) {
	for _, sequence := range []struct {
		bytes []byte
		kind  keyKind
	}{
		{[]byte("\x1b[A"), keyUp},
		{[]byte("\x1b[B"), keyDown},
		{[]byte("\x1b[C"), keyRight},
		{[]byte("\x1b[D"), keyLeft},
		{[]byte("\x1b[5~"), keyPageUp},
		{[]byte("\x1b[6~"), keyPageDown},
		{[]byte("\x1b[H"), keyHome},
		{[]byte("\x1bOH"), keyHome},
		{[]byte("\x1b[F"), keyEnd},
		{[]byte("\x1bOF"), keyEnd},
	} {
		if len(chunk) < len(sequence.bytes) && bytes.Equal(chunk, sequence.bytes[:len(chunk)]) {
			return false, true, 0, 0
		}
		if bytes.HasPrefix(chunk, sequence.bytes) {
			return true, false, sequence.kind, len(sequence.bytes)
		}
	}
	return false, false, 0, 0
}
