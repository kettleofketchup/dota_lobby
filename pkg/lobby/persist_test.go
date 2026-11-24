package lobby

import "testing"

func TestPersistKeepsTeamsUnlocked(t *testing.T) {
	l := rostered(StateWaiting, t0)
	l.Spec.TeamsUnlocked = true
	row, players, err := toRow(l)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fromRow(row, players)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Spec.TeamsUnlocked {
		t.Error("teams_unlocked lost in the round trip")
	}
}
