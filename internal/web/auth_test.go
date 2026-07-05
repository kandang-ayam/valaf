package web

import "testing"

func TestRoleAtLeast(t *testing.T) {
	cases := []struct {
		have, min string
		want      bool
	}{
		{"admin", "engineer", true},
		{"admin", "admin", true},
		{"engineer", "engineer", true},
		{"engineer", "admin", false},
		{"viewer", "engineer", false},
		{"viewer", "viewer", true},
		{"", "viewer", false},
		{"bogus", "viewer", false},
	}
	for _, c := range cases {
		if got := roleAtLeast(c.have, c.min); got != c.want {
			t.Errorf("roleAtLeast(%q,%q) = %v, want %v", c.have, c.min, got, c.want)
		}
	}
}
