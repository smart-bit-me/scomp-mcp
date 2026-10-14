package presentation

import "testing"

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
