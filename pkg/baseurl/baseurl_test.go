package baseurl

import "testing"

func TestIsLoopback(t *testing.T) {
	cases := []struct {
		base string
		want bool
	}{
		{"http://localhost:8080", true},
		{"http://LOCALHOST", true},
		{"http://localhost.:8080/cache", true},
		{"http://cache.localhost", true},
		{"http://127.0.0.1:8080", true},
		{"http://127.8.9.10", true},
		{"http://[::1]:8080", true},
		{"http://[::ffff:127.0.0.1]:8080", true},
		{"  http://localhost:8080  ", true},

		{"http://cache.example.com", false},
		{"https://cache.example.com/emule", false},
		{"http://192.168.1.20:8080", false},
		{"http://203.0.113.9", false},
		{"http://[2001:db8::1]:8080", false},
		{"http://localhost.example.com", false},
		{"http://notlocalhost", false},
		{"", false},
		{"://nope", false},
	}

	for _, tc := range cases {
		got := IsLoopback(tc.base)
		t.Logf("input:  %q", tc.base)
		t.Logf("output: %v", got)

		if got != tc.want {
			t.Errorf("IsLoopback(%q) = %v, want %v", tc.base, got, tc.want)
		}
	}
}
