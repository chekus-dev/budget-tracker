package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

/* welcomeStepFrom reads ?step= on a GET and the hidden field on a POST. The
   clamp matters more than it looks: ?step= is user-editable, and a URL that
   parsed to 0 or 99 would render a wizard with no step highlighted — a page
   that looks broken with no way to tell why. */
func TestWelcomeStepFromClamps(t *testing.T) {
	cases := []struct {
		query string
		form  string
		want  int
	}{
		{"", "", 1},                 // no step at all: start at the beginning
		{"?step=1", "", 1},          //
		{"?step=4", "", 4},          // the last real step
		{"?step=0", "", 1},          // below the range
		{"?step=-1", "", 1},         // negative
		{"?step=99", "", 1},         // past the end
		{"?step=abc", "", 1},        // not a number
		{"?step=", "", 1},           // present but empty
		{"?step=%204%20", "", 4},    // padding is trimmed
		{"?step=1", "step=3", 3},    // the POST body wins over the query
		{"?step=9", "step=2", 2},    // ...even when the query is out of range
		{"?step=2", "step=garbage", 1}, // a corrupt body falls back, not forward
	}

	for _, c := range cases {
		method := "GET"
		if c.form != "" {
			method = "POST"
		}

		req := httptest.NewRequest(method, "/welcome"+c.query, strings.NewReader(c.form))
		if method == "POST" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}

		if got := welcomeStepFrom(req); got != c.want {
			t.Errorf("welcomeStepFrom(%s %q + %q) = %d, want %d",
				method, c.query, c.form, got, c.want)
		}
	}
}

/* Exactly one step is "now" and everything before it is "done". A wizard that
   marked two steps current, or none, would still render — it would just be
   lying about where the user is. */
func TestWelcomeStepsMarksExactlyOneCurrent(t *testing.T) {
	for current := 1; current <= len(welcomeStepLabels); current++ {
		steps := welcomeSteps(current)
		if len(steps) != len(welcomeStepLabels) {
			t.Fatalf("step %d: got %d dots, want %d", current, len(steps), len(welcomeStepLabels))
		}

		now := 0
		for i, s := range steps {
			if s.Number != i+1 {
				t.Errorf("dot %d numbered %d", i, s.Number)
			}
			if s.Label != welcomeStepLabels[i] {
				t.Errorf("dot %d labelled %q, want %q", i, s.Label, welcomeStepLabels[i])
			}
			switch s.State {
			case "now":
				now++
				if s.Number != current {
					t.Errorf("step %d: dot %d is current", current, s.Number)
				}
			case "done":
				if s.Number >= current {
					t.Errorf("step %d: dot %d is done but is not behind us", current, s.Number)
				}
			case "todo":
				if s.Number <= current {
					t.Errorf("step %d: dot %d is todo but is not ahead", current, s.Number)
				}
			default:
				t.Errorf("step %d: dot %d has state %q", current, s.Number, s.State)
			}
		}
		if now != 1 {
			t.Errorf("step %d: %d dots are current, want exactly 1", current, now)
		}
	}
}

/* Step 4 is optional, which is the whole reason this is a function rather than
   three lines in the handler. "Both blank" has to come out as a skip, and
   every way of getting it wrong has to come out as something the user can act
   on rather than a 500 from the CHECK constraint on expenses.amount. */
func TestWelcomeFirstEntry(t *testing.T) {
	cases := []struct {
		description string
		amount      string
		want        welcomeEntry
		wantAmount  float64
	}{
		// The skip path, including whitespace that only looks like input.
		{"", "", welcomeEntrySkip, 0},
		{"   ", "  ", welcomeEntrySkip, 0},
		{"\t", "\n", welcomeEntrySkip, 0},

		// A complete entry.
		{"Lunch", "12.50", welcomeEntrySave, 12.50},
		{"  Lunch  ", " 12.50 ", welcomeEntrySave, 12.50},
		{"Lunch", "0.01", welcomeEntrySave, 0.01},

		// Half an entry either way is a complaint, not a save and not a skip.
		{"Lunch", "", welcomeEntryRetry, 0},
		{"", "12.50", welcomeEntryRetry, 0},

		// Amounts the column would reject. 014 put a CHECK (> 0) on
		// expenses.amount, so these have to be caught here or the insert
		// returns a 500 instead of a message.
		{"Lunch", "abc", welcomeEntryRetry, 0},
		{"Lunch", "0", welcomeEntryRetry, 0},
		{"Lunch", "-5", welcomeEntryRetry, 0},
		{"Lunch", "1,200", welcomeEntryRetry, 0}, // thousands separators are not parseable
	}

	for _, c := range cases {
		got, amount, msg := welcomeFirstEntry(c.description, c.amount)
		if got != c.want {
			t.Errorf("welcomeFirstEntry(%q, %q) = %v, want %v", c.description, c.amount, got, c.want)
		}
		if amount != c.wantAmount {
			t.Errorf("welcomeFirstEntry(%q, %q) amount = %v, want %v",
				c.description, c.amount, amount, c.wantAmount)
		}
		// A complaint with no text is a dead end: the form comes back with an
		// error box the user cannot read.
		if got == welcomeEntryRetry && strings.TrimSpace(msg) == "" {
			t.Errorf("welcomeFirstEntry(%q, %q) rejected without saying why", c.description, c.amount)
		}
		if got != welcomeEntryRetry && msg != "" {
			t.Errorf("welcomeFirstEntry(%q, %q) succeeded but carried the message %q",
				c.description, c.amount, msg)
		}
	}
}

/* The migration that makes existing accounts skip the wizard. If the backfill
   UPDATE goes missing, every account that predates the wizard is sent through
   it on its next visit — which is the one outcome this whole feature must not
   have, and it is invisible to every other test here. */
func TestOnboardingMigrationIsEmbedded(t *testing.T) {
	migration, err := migrationFS.ReadFile("migrations/017_add_onboarding.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(migration)

	if !strings.Contains(body, "ADD COLUMN IF NOT EXISTS onboarded_at") {
		t.Error("migration does not add the onboarded_at column")
	}
	if !strings.Contains(body, "UPDATE users SET onboarded_at = created_at WHERE onboarded_at IS NULL") {
		t.Error("migration does not backfill existing accounts, so every current user would be shown the wizard")
	}
}
