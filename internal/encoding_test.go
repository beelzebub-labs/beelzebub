package internal

import "testing"

func TestPlainOrBase64(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want string
	}{
		{
			name: "valid printable ASCII is returned as plain text",
			in:   []byte("hello world"),
			want: "hello world",
		},
		{
			name: "valid printable ASCII has trailing line endings trimmed",
			in:   []byte("hello world\r\n"),
			want: "hello world",
		},
		{
			name: "invalid low control byte is returned as base64",
			in:   []byte{'h', 'i', 0x00},
			want: "aGkA",
		},
		{
			name: "invalid high byte is returned as base64",
			in:   []byte{0xff, 0xfe},
			want: "//4=",
		},
		{
			name: "invalid bytes have trailing line endings trimmed before base64",
			in:   []byte{'o', 'k', 0x00, '\r', '\n'},
			want: "b2sA",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PlainOrBase64(tt.in); got != tt.want {
				t.Fatalf("PlainOrBase64(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
