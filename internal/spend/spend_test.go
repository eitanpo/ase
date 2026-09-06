package spend

import (
	"testing"

	"github.com/eitanpo/agentry/internal/model"
)

func TestTokens(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"}, {999, "999"}, {1000, "1.0k"}, {1500, "1.5k"}, {9999, "10.0k"}, {15000, "15k"},
	}
	for _, tt := range tests {
		if got := Tokens(tt.n); got != tt.want {
			t.Errorf("Tokens(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

// TestLineOmitsAbsentCost pins the difference between a session that cost
// nothing and one whose log records no cost. Both would render "$0.00" if the
// field were a plain float, and a reader could not tell a measurement from a
// gap.
func TestLineOmitsAbsentCost(t *testing.T) {
	u := model.Usage{Input: 10, Output: 2000, CacheRead: 90, CacheCreate: 0}
	if got := Line(u, nil, nil, nil); got != "Tokens: 10 in / 2.0k out  ·  cache 90%" {
		t.Errorf("Line without cost = %q", got)
	}
	zero := 0.0
	if got := Line(u, &zero, nil, nil); got != "Tokens: 10 in / 2.0k out  ·  cache 90%  ·  $0.00" {
		t.Errorf("Line with a recorded zero = %q; a measured zero must render", got)
	}
}

// TestLineRoundsToTheCent pins that Claude Code's own precision is not passed
// through. Its running total carries fifteen decimal places, which would claim
// an accuracy the number does not have.
func TestLineRoundsToTheCent(t *testing.T) {
	c := 17.254517250000003
	want := "Tokens: 0 in / 0 out  ·  $17.25"
	if got := Line(model.Usage{}, &c, nil, nil); got != want {
		t.Errorf("Line = %q, want %q", got, want)
	}
}

// TestLineShowsLinesChanged pins how much code a session changed, and the one
// case that is dropped: both counters zero. Two thirds of local sessions with a
// cost record changed no lines, so rendering "+0/-0" on every one of them would
// spend the line on nothing — and the dollar figure beside it already says the
// record exists, since one entry carries both.
func TestLineShowsLinesChanged(t *testing.T) {
	cost, add, rem := 17.25, 342, 8
	got := Line(model.Usage{Input: 5, Output: 9}, &cost, &add, &rem)
	if want := "Tokens: 5 in / 9 out  ·  cache 0%  ·  $17.25  ·  +342/-8"; got != want {
		t.Errorf("Line = %q, want %q", got, want)
	}

	t.Run("removals alone still render", func(t *testing.T) {
		zero, rem := 0, 12
		got := Line(model.Usage{}, nil, &zero, &rem)
		if want := "Tokens: 0 in / 0 out  ·  +0/-12"; got != want {
			t.Errorf("Line = %q, want %q", got, want)
		}
	})

	t.Run("a session that changed nothing shows no counters", func(t *testing.T) {
		cost, zero := 4.0, 0
		got := Line(model.Usage{}, &cost, &zero, &zero)
		if want := "Tokens: 0 in / 0 out  ·  $4.00"; got != want {
			t.Errorf("Line = %q, want %q", got, want)
		}
	})
}
