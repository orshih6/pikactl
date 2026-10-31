package output

import "testing"

func TestUSDNeverRounds(t *testing.T) {
	tests := map[int64]string{
		0:          "$0.00",
		200000:     "$0.20",
		372189:     "$0.372189",
		1000:       "$0.001",
		200:        "$0.0002",
		1:          "$0.000001",
		1_000_000:  "$1.00",
		12_345_678: "$12.345678",
		-500000:    "-$0.50",
	}
	for micro, want := range tests {
		if got := USD(micro); got != want {
			t.Errorf("USD(%d) = %q, want %q", micro, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("a  b\nc", 10); got != "a b c" {
		t.Errorf("Truncate = %q", got)
	}
	if got := Truncate("сайн байна уу", 5); got != "сайн…" {
		t.Errorf("Truncate = %q: it must cut characters, not bytes", got)
	}
}
