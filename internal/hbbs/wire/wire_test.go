package wire

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

// Expected bytes below are derived by hand from the rules hbb_common applies
// (libs/hbb_common/src/bytes_codec.rs encode/decode_head; protobuf encoding of
// RendezvousMessage with online_request = 23, online_response = 24,
// key_exchange = 25), not produced by this package:
//
//   - OnlineRequest{peers:["123456789"]}:
//     inner  = tag(2,LEN)=0x12, len 9, "123456789"            -> 11 bytes
//     outer  = tag(23,LEN)=(23<<3|2)=186 -> varint 0xBA 0x01, len 0x0B, inner
//     frame  = 14-byte payload <= 0x3F -> header 14<<2 = 0x38
//   - OnlineResponse{states:[0x80]}:
//     inner  = tag(1,LEN)=0x0A, len 1, 0x80
//     outer  = tag(24,LEN)=194 -> 0xC2 0x01, len 3, inner
//     frame  = header 6<<2 = 0x18
//   - KeyExchange{keys:[96 bytes]} (hbbs sends sign(pk) = 64-byte sig + 32-byte pk):
//     inner  = 0x0A 0x60 <96 bytes>                            -> 98 bytes
//     outer  = tag(25,LEN)=202 -> 0xCA 0x01, len 0x62, inner   -> 101 bytes
//     frame  = 101 > 0x3F -> u16le(101<<2|1 = 0x195) = 0x95 0x01
func TestGoldenBytes(t *testing.T) {
	req, err := AppendFrame(nil, EncodeOnlineRequest([]string{"123456789"}))
	if err != nil {
		t.Fatal(err)
	}
	want := "38" + "ba01" + "0b" + "12" + "09" + hex.EncodeToString([]byte("123456789"))
	if got := hex.EncodeToString(req); got != want {
		t.Fatalf("online request\n got %s\nwant %s", got, want)
	}

	resp, _ := AppendFrame(nil, EncodeOnlineResponse([]byte{0x80}))
	if got := hex.EncodeToString(resp); got != "18c201030a0180" {
		t.Fatalf("online response: %s", got)
	}

	ke, _ := AppendFrame(nil, EncodeKeyExchange(bytes.Repeat([]byte{0xAB}, 96)))
	if got := hex.EncodeToString(ke[:6]); got != "9501ca01620a" || len(ke) != 2+101 {
		t.Fatalf("key exchange header: %s len %d", got, len(ke))
	}
	if got := hex.EncodeToString(ke[6:7]); got != "60" {
		t.Fatalf("key length byte: %s", got)
	}

	// Empty request (used as a health probe): RendezvousMessage{online_request{}}.
	empty, _ := AppendFrame(nil, EncodeOnlineRequest(nil))
	if got := hex.EncodeToString(empty); got != "0cba0100" {
		t.Fatalf("empty request: %s", got)
	}
}

func TestFrameHeaderSizes(t *testing.T) {
	for _, tc := range []struct {
		n      int
		header string
	}{
		{0, "00"},
		{0x3F, "fc"},
		{0x40, "0101"},
		{0x3FFF, "fdff"},
		{0x4000, "020001"},
		{0x3FFFFF, "feffff"},
		{0x400000, "03000001"},
	} {
		payload := bytes.Repeat([]byte{7}, tc.n)
		f, err := AppendFrame(nil, payload)
		if err != nil {
			t.Fatal(err)
		}
		hl := len(f) - tc.n
		if got := hex.EncodeToString(f[:hl]); got != tc.header {
			t.Errorf("n=%#x header %s want %s", tc.n, got, tc.header)
		}
		back, err := ReadFrame(bytes.NewReader(f), MaxFrameLen)
		if err != nil || len(back) != tc.n {
			t.Errorf("n=%#x roundtrip len %d err %v", tc.n, len(back), err)
		}
	}
}

func TestReadFrameErrors(t *testing.T) {
	f, _ := AppendFrame(nil, bytes.Repeat([]byte{1}, 100))
	if _, err := ReadFrame(bytes.NewReader(f), 99); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("limit: %v", err)
	}
	if _, err := ReadFrame(bytes.NewReader(f[:50]), 1000); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated body: %v", err)
	}
	if _, err := ReadFrame(bytes.NewReader(f[:1]), 1000); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated header: %v", err)
	}
	if _, err := ReadFrame(bytes.NewReader(nil), 1000); !errors.Is(err, io.EOF) {
		t.Fatalf("empty: %v", err)
	}
	// Several frames back to back.
	var buf []byte
	buf, _ = AppendFrame(buf, []byte("a"))
	buf, _ = AppendFrame(buf, []byte("bc"))
	r := bytes.NewReader(buf)
	a, _ := ReadFrame(r, 10)
	b, _ := ReadFrame(r, 10)
	if string(a) != "a" || string(b) != "bc" {
		t.Fatalf("sequence: %q %q", a, b)
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	peers := []string{"123456789", "abcdef", strings.Repeat("x", 100)}
	env, err := DecodeEnvelope(EncodeOnlineRequest(peers))
	if err != nil || env.Field != FieldOnlineRequest {
		t.Fatalf("%+v %v", env, err)
	}
	got, err := DecodeOnlineRequest(env.Body)
	if err != nil || strings.Join(got, ",") != strings.Join(peers, ",") {
		t.Fatalf("peers %v %v", got, err)
	}

	env, err = DecodeEnvelope(EncodeOnlineResponse([]byte{0xA0, 0x01}))
	if err != nil || env.Field != FieldOnlineResponse {
		t.Fatalf("%+v %v", env, err)
	}
	states, err := DecodeOnlineResponse(env.Body)
	if err != nil || !bytes.Equal(states, []byte{0xA0, 0x01}) {
		t.Fatalf("states %x %v", states, err)
	}

	// Empty states (all peers offline with n=0) -> field omitted.
	env, _ = DecodeEnvelope(EncodeOnlineResponse(nil))
	if env.Field != FieldOnlineResponse || len(env.Body) != 0 {
		t.Fatalf("empty response %+v", env)
	}
}

func TestDecodeSkipsUnknownFields(t *testing.T) {
	// varint field 1 = 150, fixed64 field 2, fixed32 field 3, then online_response.
	msg, _ := hex.DecodeString("089601" + "110102030405060708" + "1d01020304" + "c201030a01ff")
	env, err := DecodeEnvelope(msg)
	if err != nil || env.Field != FieldOnlineResponse {
		t.Fatalf("%+v %v", env, err)
	}
}

func TestDecodeMalformed(t *testing.T) {
	for _, h := range []string{
		"c2",       // truncated tag varint
		"c20105",   // length beyond buffer
		"0b",       // group start wire type
		"08",       // varint field without value
		"11010203", // short fixed64
		"00",       // field number 0
		"ffffffffffffffffffff01",
	} {
		b, _ := hex.DecodeString(h)
		if _, err := DecodeEnvelope(b); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err %v", h, err)
		}
	}
}

func TestBitmap(t *testing.T) {
	if BitmapLen(0) != 0 || BitmapLen(1) != 1 || BitmapLen(8) != 1 || BitmapLen(9) != 2 {
		t.Fatal("BitmapLen")
	}
	// hbbs: states[i/8] |= 0x01 << (7 - i%8)
	states := make([]byte, 2)
	for _, i := range []int{0, 7, 9} {
		SetOnline(states, i)
	}
	if !bytes.Equal(states, []byte{0x81, 0x40}) {
		t.Fatalf("%x", states)
	}
	for i := 0; i < 16; i++ {
		want := i == 0 || i == 7 || i == 9
		if Online(states, i) != want {
			t.Errorf("bit %d", i)
		}
	}
	if Online(states, 16) || Online(states, -1) {
		t.Fatal("out of range must be offline")
	}
}

func FuzzDecodeEnvelope(f *testing.F) {
	f.Add(EncodeOnlineResponse([]byte{1, 2}))
	f.Add(EncodeKeyExchange([]byte("k")))
	f.Fuzz(func(t *testing.T, b []byte) {
		env, err := DecodeEnvelope(b)
		if err == nil && env.Field == FieldOnlineResponse {
			_, _ = DecodeOnlineResponse(env.Body)
		}
	})
}
