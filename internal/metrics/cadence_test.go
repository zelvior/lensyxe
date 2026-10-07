package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/zelvior/lensyxe/pkg/models"
)

// Withholding is the point. Scoring 266 commits/week at face value would award
// full marks to every repository created yesterday, which is the mirror image
// of the bug the span fix replaced -- a low reading on a young project is a
// statement about the measurement, not about the team.
func TestCadenceIsWithheldWhenHistoryIsTooShort(t *testing.T) {
	g := models.GitStats{
		IsRepository:    true,
		TotalCommits:    38,
		WindowCommits:   38,
		Authors:         1,
		BusFactor:       1,
		LastCommitAt:    time.Now(),
		FirstCommitAt:   time.Now().Add(-24 * time.Hour),
		CadenceSpanDays: 1,
		CommitsPerWeek:  266,
	}

	m := scoreGit(g)
	if !m.Applicable {
		t.Fatal("the git metric should still apply")
	}
	if !strings.Contains(m.Detail, "cadence not judged") {
		t.Errorf("the withheld cadence is not explained: %q", m.Detail)
	}
	if !strings.Contains(m.Detail, "28-day") {
		t.Errorf("the reason does not state the minimum: %q", m.Detail)
	}
	// The three remaining signals are freshness 100, churn 100, bus 85, which
	// renormalize to 95. The assertion is a floor rather than an equality because
	// the point under test is that a withheld component neither zeroes the
	// metric nor leaves it understated.
	if m.Score < 90 {
		t.Errorf("score = %v; the three measured signals should carry it: %s",
			m.Score, m.Detail)
	}
}

// Withholding must not quietly improve the score without saying so. A reader
// auditing the number has to be able to see that a component was dropped.
func TestWithheldCadenceNamesTheRemainingSignals(t *testing.T) {
	g := models.GitStats{
		IsRepository:    true,
		TotalCommits:    10,
		WindowCommits:   10,
		Authors:         1,
		BusFactor:       1,
		LastCommitAt:    time.Now(),
		FirstCommitAt:   time.Now().Add(-72 * time.Hour),
		CadenceSpanDays: 3,
		CommitsPerWeek:  23.33,
	}
	m := scoreGit(g)
	for _, want := range []string{"freshness", "churn", "bus factor"} {
		if !strings.Contains(m.Detail, want) {
			t.Errorf("detail omits %q: %s", want, m.Detail)
		}
	}
}

// Once there is real history the cadence is judged again, and the detail must
// report it rather than the withheld wording.
func TestCadenceIsJudgedOnceHistoryIsLongEnough(t *testing.T) {
	g := models.GitStats{
		IsRepository:    true,
		TotalCommits:    200,
		WindowCommits:   200,
		Authors:         3,
		BusFactor:       2,
		LastCommitAt:    time.Now(),
		FirstCommitAt:   time.Now().Add(-120 * 24 * time.Hour),
		CadenceSpanDays: 90,
		CommitsPerWeek:  14,
	}
	m := scoreGit(g)
	if strings.Contains(m.Detail, "not judged") {
		t.Fatalf("cadence was withheld despite %d days of history: %s",
			g.CadenceSpanDays, m.Detail)
	}
	if !strings.Contains(m.Detail, "cadence ") {
		t.Errorf("the cadence component is missing from the detail: %s", m.Detail)
	}
}

// A genuinely slow repository must still be penalised. The gate is on history
// depth, not on the rate -- otherwise "too young to judge" becomes a universal
// excuse and the component stops meaning anything.
func TestASlowRepositoryWithHistoryIsStillPenalised(t *testing.T) {
	g := models.GitStats{
		IsRepository:    true,
		TotalCommits:    8,
		WindowCommits:   8,
		Authors:         2,
		BusFactor:       1,
		LastCommitAt:    time.Now(),
		FirstCommitAt:   time.Now().Add(-400 * 24 * time.Hour),
		CadenceSpanDays: 90,
		CommitsPerWeek:  0.6, // just above LowCadenceFloor
	}
	m := scoreGit(g)
	if strings.Contains(m.Detail, "not judged") {
		t.Fatalf("cadence was withheld on a repository with 400 days of history: %s",
			m.Detail)
	}
	if m.Score >= 90 {
		t.Errorf("a 0.6/week repository scored %v; the gate is on history depth, "+
			"not on excusing slow rates", m.Score)
	}
}

// The floor has to be a number a reader can check, and it has to be more than a
// week: a week is the unit being estimated, so averaging over one week estimates
// one number with one sample.
func TestTheCadenceFloorIsMoreThanOneWeek(t *testing.T) {
	if MinCadenceSpanDays < 7 {
		t.Errorf("MinCadenceSpanDays = %d; a weekly rate cannot be estimated "+
			"from less than a week", MinCadenceSpanDays)
	}
}
