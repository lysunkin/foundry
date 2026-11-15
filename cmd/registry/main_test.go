package main

import "testing"

func TestIsLoopback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"127.1.2.3:8080", true},
		{"[::1]:8080", true},
		{"localhost:8080", true},
		{":8080", false},
		{"0.0.0.0:8080", false},
		{"[::]:8080", false},
		{"192.168.1.10:8080", false},
		{"example.com:8080", false},
		{"garbage", false},
	}
	for _, tc := range tests {
		if got := isLoopback(tc.addr); got != tc.want {
			t.Errorf("isLoopback(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestEnvBool(t *testing.T) {
	tests := []struct {
		name, val     string
		fallback      bool
		want, wantErr bool
	}{
		{"unset uses fallback", "", true, true, false},
		{"false", "false", true, false, false},
		{"zero", "0", true, false, false},
		{"true", "true", false, true, false},
		{"invalid", "yes please", true, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FOUNDRY_TEST_BOOL", tc.val)
			got, err := envBool("FOUNDRY_TEST_BOOL", tc.fallback)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("envBool(%q) = %v, %v; want %v, err=%v", tc.val, got, err, tc.want, tc.wantErr)
			}
		})
	}
}
