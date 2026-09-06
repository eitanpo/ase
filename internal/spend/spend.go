// Package spend phrases what a session amounted to — its tokens, its cost, and
// the lines it changed — in the one wording both the rendered header and the
// listing's cost channel print. The two surfaces must not disagree about a single
// session, and the cheapest guarantee of that is one string that neither of them
// composes for itself.
package spend

import (
	"fmt"

	"github.com/eitanpo/agentry/internal/model"
)

// Line reads "Tokens: 516 in / 323k out  ·  cache 96%  ·  $17.25  ·  +342/-8".
// The cache share is dropped when nothing was sent to be cached or read back, and
// the dollar figure when the log recorded no cost: a session Claude Code wrote no
// cost for is not a session that cost nothing, so no zero stands in for it.
//
// The line counters are dropped when both are zero, which reads as "changed no
// code" rather than as "not recorded" — the dollar figure beside them already
// says whether the record exists, since one entry carries both, and two thirds of
// local sessions with a record changed no lines at all.
func Line(u model.Usage, costUSD *float64, linesAdded, linesRemoved *int) string {
	s := fmt.Sprintf("Tokens: %s in / %s out", Tokens(u.Input), Tokens(u.Output))
	if in := u.Input + u.CacheRead + u.CacheCreate; in > 0 {
		s += fmt.Sprintf("  ·  cache %.0f%%", float64(u.CacheRead)/float64(in)*100)
	}
	if costUSD != nil {
		s += "  ·  " + USD(*costUSD)
	}
	if add, rem := deref(linesAdded), deref(linesRemoved); add != 0 || rem != 0 {
		s += fmt.Sprintf("  ·  +%d/-%d", add, rem)
	}
	return s
}

func deref(n *int) int {
	if n == nil {
		return 0
	}
	return *n
}

// USD renders a dollar amount to the cent. Claude Code's own record carries far
// more precision than that — one local session reads 17.254517250000003 — and
// printing every digit would claim an accuracy the number does not have.
func USD(v float64) string { return fmt.Sprintf("$%.2f", v) }

// Tokens abbreviates a token count to fit a column: exact below a thousand, then
// thousands to one decimal place up to ten thousand and to none above it.
func Tokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 10000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%.0fk", float64(n)/1000)
}
