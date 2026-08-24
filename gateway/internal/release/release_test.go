package release

import (
	"encoding/hex"
	"testing"
)

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"0.1.0", "0.1.0", 0},
		{"0.1.0", "0.2.0", -1},
		{"0.2.0", "0.1.0", 1},
		{"0.10.0", "0.9.0", 1},
		{"v0.1.0", "v0.2.0", -1},
		{"dev", "0.1.0", -1},
		{"0.1.0", "dev", 1},
		{"1.0.0", "1.0.0", 0},
		{"1.0", "1.0.0", 0},
	}
	for _, tt := range tests {
		if got := CompareSemver(tt.a, tt.b); got != tt.want {
			t.Errorf("CompareSemver(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestIsHex64(t *testing.T) {
	valid := hex.EncodeToString(make([]byte, 32))
	if !isHex64(valid) {
		t.Error("64 lowercase hex should be valid")
	}
	if isHex64("") {
		t.Error("empty should be invalid")
	}
	if isHex64("ABCDEF") {
		t.Error("uppercase should be invalid")
	}
	if isHex64(valid[:63]) {
		t.Error("63 chars should be invalid")
	}
}
