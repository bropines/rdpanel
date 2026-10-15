// Package wire hand-encodes the tiny subset of the hbbs rendezvous protocol the
// panel needs: the length-prefixed TCP framing used by hbbs/hbbr and the
// RendezvousMessage envelope carrying OnlineRequest / OnlineResponse /
// KeyExchange.
//
// This is a clean-room implementation written from the protocol description in
// docs/research/hbbs-integration.md (§2.3); no .proto files or generated code
// are used.
//
// # Framing
//
// Every TCP message is prefixed with a little-endian variable-length header.
// The low two bits of the first byte hold (header length - 1); the remaining
// bits hold the payload length:
//
//	len <= 0x3F       1 byte   len<<2
//	len <= 0x3FFF     2 bytes  u16le(len<<2 | 1)
//	len <= 0x3FFFFF   3 bytes  u24le(len<<2 | 2)
//	len <= 0x3FFFFFFF 4 bytes  u32le(len<<2 | 3)
//
// # Messages
//
// RendezvousMessage is a protobuf message whose oneof carries, among others:
//
//	field 23  OnlineRequest  { string id = 1; repeated string peers = 2; }
//	field 24  OnlineResponse { bytes states = 1; }
//	field 25  KeyExchange    { repeated bytes keys = 1; }
//
// OnlineResponse.states is a bitmap with ceil(n/8) bytes: peer i is online when
// states[i/8] & (0x80 >> (i%8)) is set (MSB first).
package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// RendezvousMessage oneof field numbers used by the panel.
const (
	FieldOnlineRequest  = 23
	FieldOnlineResponse = 24
	FieldKeyExchange    = 25
)

// MaxFrameLen is the largest payload the framing can express.
const MaxFrameLen = 0x3FFFFFFF

// Protobuf wire types.
const (
	wtVarint = 0
	wtI64    = 1
	wtLen    = 2
	wtI32    = 5
)

var (
	// ErrFrameTooLarge is returned when a frame exceeds the caller's limit.
	ErrFrameTooLarge = errors.New("wire: frame too large")
	// ErrMalformed is returned for undecodable protobuf data.
	ErrMalformed = errors.New("wire: malformed message")
)

// AppendFrame appends payload to dst with the hbb_common length header.
func AppendFrame(dst, payload []byte) ([]byte, error) {
	n := len(payload)
	switch {
	case n <= 0x3F:
		dst = append(dst, byte(n<<2))
	case n <= 0x3FFF:
		dst = binary.LittleEndian.AppendUint16(dst, uint16(n<<2|1))
	case n <= 0x3FFFFF:
		h := uint32(n<<2 | 2)
		dst = append(dst, byte(h), byte(h>>8), byte(h>>16))
	case n <= MaxFrameLen:
		dst = binary.LittleEndian.AppendUint32(dst, uint32(n<<2|3))
	default:
		return dst, ErrFrameTooLarge
	}
	return append(dst, payload...), nil
}

// ReadFrame reads one framed payload from r. Payloads larger than maxLen are
// rejected before allocation.
func ReadFrame(r io.Reader, maxLen int) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:1]); err != nil {
		return nil, err
	}
	hl := int(hdr[0]&3) + 1
	if hl > 1 {
		if _, err := io.ReadFull(r, hdr[1:hl]); err != nil {
			return nil, unexpected(err)
		}
	}
	n := int(binary.LittleEndian.Uint32(hdr[:]) >> 2)
	if n > maxLen {
		return nil, fmt.Errorf("%w: %d > %d", ErrFrameTooLarge, n, maxLen)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, unexpected(err)
	}
	return buf, nil
}

func unexpected(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// EncodeOnlineRequest returns RendezvousMessage{online_request{peers}} (unframed).
// The OnlineRequest.id field is ignored by hbbs and therefore omitted.
func EncodeOnlineRequest(peers []string) []byte {
	inner := make([]byte, 0, 16*len(peers))
	for _, p := range peers {
		inner = appendLenField(inner, 2, []byte(p))
	}
	return appendLenField(make([]byte, 0, len(inner)+8), FieldOnlineRequest, inner)
}

// EncodeOnlineResponse returns RendezvousMessage{online_response{states}}
// (unframed). Used by test doubles of hbbs.
func EncodeOnlineResponse(states []byte) []byte {
	var inner []byte
	if len(states) > 0 { // proto3: empty bytes are not serialized
		inner = appendLenField(nil, 1, states)
	}
	return appendLenField(nil, FieldOnlineResponse, inner)
}

// EncodeKeyExchange returns RendezvousMessage{key_exchange{keys}} (unframed).
// Used by test doubles of hbbs.
func EncodeKeyExchange(keys ...[]byte) []byte {
	var inner []byte
	for _, k := range keys {
		inner = appendLenField(inner, 1, k)
	}
	return appendLenField(nil, FieldKeyExchange, inner)
}

// Envelope is a decoded RendezvousMessage: the number of the oneof field that
// was set (0 if none) and its raw message bytes.
type Envelope struct {
	Field int
	Body  []byte
}

// DecodeEnvelope finds the oneof member of a RendezvousMessage. Unknown fields
// are skipped. As in protobuf, the last occurrence of a oneof member wins.
func DecodeEnvelope(msg []byte) (Envelope, error) {
	var env Envelope
	err := walk(msg, func(num int, wt int, val []byte) error {
		if wt == wtLen {
			env = Envelope{Field: num, Body: val}
		}
		return nil
	})
	return env, err
}

// DecodeOnlineRequest returns the peers of an OnlineRequest body. Used by
// test doubles of hbbs.
func DecodeOnlineRequest(body []byte) ([]string, error) {
	var peers []string
	err := walk(body, func(num int, wt int, val []byte) error {
		if num == 2 && wt == wtLen {
			peers = append(peers, string(val))
		}
		return nil
	})
	return peers, err
}

// DecodeOnlineResponse returns OnlineResponse.states.
func DecodeOnlineResponse(body []byte) ([]byte, error) {
	var states []byte
	err := walk(body, func(num int, wt int, val []byte) error {
		if num == 1 && wt == wtLen {
			states = val
		}
		return nil
	})
	return states, err
}

// BitmapLen is the number of state bytes hbbs returns for n peers.
func BitmapLen(n int) int { return (n + 7) / 8 }

// Online reports bit i of an OnlineResponse bitmap (MSB first).
func Online(states []byte, i int) bool {
	if i < 0 || i/8 >= len(states) {
		return false
	}
	return states[i/8]&(0x80>>(uint(i)%8)) != 0
}

// SetOnline sets bit i of a bitmap (test helper for fake servers).
func SetOnline(states []byte, i int) {
	states[i/8] |= 0x80 >> (uint(i) % 8)
}

func appendLenField(dst []byte, num int, val []byte) []byte {
	dst = binary.AppendUvarint(dst, uint64(num)<<3|wtLen)
	dst = binary.AppendUvarint(dst, uint64(len(val)))
	return append(dst, val...)
}

// walk iterates the top-level fields of a protobuf message. val is the
// payload for length-delimited fields and nil otherwise.
func walk(b []byte, fn func(num int, wt int, val []byte) error) error {
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return ErrMalformed
		}
		b = b[n:]
		num, wt := int(key>>3), int(key&7)
		if num <= 0 || key>>3 > 1<<29-1 {
			return ErrMalformed
		}
		var val []byte
		switch wt {
		case wtVarint:
			_, n = binary.Uvarint(b)
			if n <= 0 {
				return ErrMalformed
			}
			b = b[n:]
		case wtI64:
			if len(b) < 8 {
				return ErrMalformed
			}
			b = b[8:]
		case wtI32:
			if len(b) < 4 {
				return ErrMalformed
			}
			b = b[4:]
		case wtLen:
			l, n := binary.Uvarint(b)
			if n <= 0 || l > uint64(len(b)-n) {
				return ErrMalformed
			}
			val = b[n : n+int(l)]
			b = b[n+int(l):]
		default: // groups (3, 4) are not used by these messages
			return ErrMalformed
		}
		if err := fn(num, wt, val); err != nil {
			return err
		}
	}
	return nil
}
