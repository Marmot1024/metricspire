package main

import "testing"

func TestAppListenAddress(t *testing.T) {
	for _, test := range []struct {
		value     string
		want      string
		wantError bool
	}{
		{value: "8080", want: "0.0.0.0:8080"},
		{value: " 9000 ", want: "0.0.0.0:9000"},
		{value: "", wantError: true},
		{value: "0", wantError: true},
		{value: "65536", wantError: true},
		{value: "not-a-port", wantError: true},
	} {
		got, err := appListenAddress(test.value)
		if test.wantError {
			if err == nil {
				t.Fatalf("appListenAddress(%q) = %q, want error", test.value, got)
			}
			continue
		}
		if err != nil || got != test.want {
			t.Fatalf("appListenAddress(%q) = %q, %v; want %q", test.value, got, err, test.want)
		}
	}
}
