package api

import "testing"

func TestIsLoopback(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8090", true},
		{"127.0.0.53:8090", true},
		{"localhost:8090", true},
		{"[::1]:8090", true},
		{":8090", false},
		{"0.0.0.0:8090", false},
		{"10.0.0.5:8090", false},
	} {
		if got := isLoopback(tc.addr); got != tc.want {
			t.Errorf("isLoopback(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}
