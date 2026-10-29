package presentation

import (
	"bytes"
	"strings"
	"testing"
)

func TestNormalizerSplitSequencesAndUTF8(t *testing.T) {
	var normalizer Normalizer
	parts := [][]byte{
		[]byte("prompt \x1b[31"),
		[]byte("mred\x1b[0m\r\n"),
		[]byte("\x1b]0;secret title\x1b"),
		append([]byte("\\ok "), 0xe2),
		[]byte{0x82, 0xac},
	}
	var got string
	for _, part := range parts {
		got += string(normalizer.Feed(part))
	}
	if want := "prompt red\nok €"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNormalizerReplacesInvalidAndDropsControls(t *testing.T) {
	var normalizer Normalizer
	got := string(normalizer.Feed([]byte{'a', 0, 0xff, 'b', '\t', 'c'}))
	if got != "a�b\tc" {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizerDropsEscapeFamilies(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"a\x1b(Bb\x1b[m", "ab"},                                 // charset designation, as tput sgr0 emits
		{"a\x1b)0\x1b#8b", "ab"},                                 // intermediate bytes
		{"a\x1bPq#0;2;0;0;0\x1b\\b", "ab"},                       // DCS (sixel)
		{"a\x1bXsos\x1b\\b\x1b^pm\x1b\\c", "abc"},                // SOS, PM
		{"a\x1b_Gf=24;payload\x1b\\b", "ab"},                     // APC (kitty graphics)
		{"a\x1b]52;c;c2VjcmV0\x07b", "ab"},                       // OSC 52 clipboard, BEL
		{"a\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\b", "alinkb"}, // OSC 8 hyperlink, ST
		{"a\u009b31mb\u0090c", "a31mbc"},                         // C1 controls are dropped, not interpreted
		{"a\x1b[31\x1b[0mb", "ab"},                               // ESC restarts inside CSI
		{"a\x1b7b\x1b8c\x1b=d", "abcd"},                          // two-byte escapes
	} {
		var n Normalizer
		if got := string(n.Feed([]byte(tc.in))); got != tc.want {
			t.Errorf("Feed(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizerBoundsUnterminatedSequences(t *testing.T) {
	var n Normalizer
	csi := "\x1b[" + strings.Repeat("1;", maxCSIBytes) + "visible"
	if got := string(n.Feed([]byte(csi))); !strings.HasSuffix(got, "visible") {
		t.Fatalf("unterminated CSI hid later output: %q", got)
	}
	n.Reset()
	osc := append([]byte("\x1b]0;"), bytes.Repeat([]byte("x"), maxStringBytes+1)...)
	osc = append(osc, "visible"...)
	if got := string(n.Feed(osc)); !strings.HasSuffix(got, "visible") || len(got) > 16 {
		t.Fatalf("unterminated string: %q", got)
	}
}

func TestNormalizerStringTerminatorSplitAcrossFeeds(t *testing.T) {
	var n Normalizer
	got := string(n.Feed([]byte("a\x1bPdata\x1b"))) + string(n.Feed([]byte("\\b")))
	if got != "ab" {
		t.Fatalf("got %q", got)
	}
}
