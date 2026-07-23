package greeting

import "testing"

func TestGreet(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "with name", in: "Nick", want: "Hello, Nick!"},
		{name: "empty falls back to World", in: "", want: "Hello, World!"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Greet(tt.in); got != tt.want {
				t.Errorf("Greet(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
