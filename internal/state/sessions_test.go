package state

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// A session's history replays: what it changed, whether a test was touched,
// whether the Gate passed, whether that verdict still describes the tree, and
// what it showed after its worktree was removed.
func TestSessionReplay(t *testing.T) {
	obs := func(fp string, files, tests []string) api.SessionObservedPayload {
		return api.SessionObservedPayload{
			SessionID: "s-1", Path: "/repo/.wt/login", Branch: "feat/login",
			Head: "h1", Base: "b0", Files: files, TestFiles: tests, Fingerprint: fp,
		}
	}
	tests := []struct {
		name      string
		build     func(b *build) *build
		wantFiles []string
		wantTests []string
		wantCheck string // "", "pass", "fail"
		wantStale bool
		wantGone  bool
	}{
		{
			name:      "observed",
			build:     func(b *build) *build { return b.add(api.SessionObserved, obs("f1", []string{"auth.go"}, nil)) },
			wantFiles: []string{"auth.go"},
		},
		{
			name: "a test file touched is kept",
			build: func(b *build) *build {
				return b.add(api.SessionObserved, obs("f1", []string{"auth.go", "auth_test.go"}, []string{"auth_test.go"}))
			},
			wantFiles: []string{"auth.go", "auth_test.go"},
			wantTests: []string{"auth_test.go"},
		},
		{
			name: "checked green at the current fingerprint",
			build: func(b *build) *build {
				return b.add(api.SessionObserved, obs("f1", []string{"auth.go"}, nil)).
					add(api.SessionChecked, api.SessionCheckedPayload{SessionID: "s-1", Fingerprint: "f1", Passed: true, Command: "go test ./..."})
			},
			wantFiles: []string{"auth.go"},
			wantCheck: "pass",
		},
		{
			name: "a check goes stale when the tree changes after it",
			build: func(b *build) *build {
				return b.add(api.SessionObserved, obs("f1", []string{"auth.go"}, nil)).
					add(api.SessionChecked, api.SessionCheckedPayload{SessionID: "s-1", Fingerprint: "f1", Passed: false, Command: "go test ./..."}).
					add(api.SessionObserved, obs("f2", []string{"auth.go", "db.go"}, nil))
			},
			wantFiles: []string{"auth.go", "db.go"},
			wantCheck: "fail",
			wantStale: true,
		},
		{
			name: "removal keeps what the session last showed",
			build: func(b *build) *build {
				return b.add(api.SessionObserved, obs("f1", []string{"auth.go"}, []string{"auth_test.go"})).
					add(api.SessionObserved, api.SessionObservedPayload{SessionID: "s-1", Path: "/repo/.wt/login", Removed: true})
			},
			wantFiles: []string{"auth.go"},
			wantTests: []string{"auth_test.go"},
			wantGone:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.build(newBuild(t)).fold()
			x := s.Sessions["s-1"]
			if x == nil {
				t.Fatal("session s-1 not folded")
			}
			if !reflect.DeepEqual(x.Files, tt.wantFiles) || !reflect.DeepEqual(x.TestFiles, tt.wantTests) {
				t.Errorf("files %v tests %v, want %v and %v", x.Files, x.TestFiles, tt.wantFiles, tt.wantTests)
			}
			got := ""
			if x.Check != nil {
				got = map[bool]string{true: "pass", false: "fail"}[x.Check.Passed]
			}
			if got != tt.wantCheck {
				t.Errorf("check = %q, want %q", got, tt.wantCheck)
			}
			if x.CheckStale() != tt.wantStale {
				t.Errorf("CheckStale = %v, want %v", x.CheckStale(), tt.wantStale)
			}
			if x.Removed != tt.wantGone {
				t.Errorf("Removed = %v, want %v", x.Removed, tt.wantGone)
			}
		})
	}
}

// Sessions survive a StateSnapshot, and a snapshot taken before any session
// existed still accepts one — the map is created on first use.
func TestSessionsSurviveSnapshot(t *testing.T) {
	s := newBuild(t).
		add(api.SessionObserved, api.SessionObservedPayload{SessionID: "s-1", Path: "/wt/a", Fingerprint: "f1"}).
		fold()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	restored := newBuild(t).add(api.StateSnapshot, api.StateSnapshotPayload{State: raw}).fold()
	if x := restored.Sessions["s-1"]; x == nil || x.Fingerprint != "f1" {
		t.Fatalf("session lost across snapshot: %+v", restored.Sessions)
	}

	old := New()
	old.Sessions = nil
	if err := old.Apply(mustEvent(t, api.SessionObserved, api.SessionObservedPayload{SessionID: "s-2", Path: "/wt/b"})); err != nil {
		t.Fatalf("apply onto a pre-sessions snapshot: %v", err)
	}
	if old.Sessions["s-2"] == nil {
		t.Error("session not recorded onto a state with no Sessions map")
	}
}

// A verdict holds for the tree the Gate saw and for the tree it left behind
// with its own output added; any other tree makes it stale.
func TestCheckStaleWithAfterFingerprint(t *testing.T) {
	tests := []struct {
		name    string
		current string
		after   string
		want    bool
	}{
		{"the tree the Gate saw", "f1", "", false},
		{"changed, no after", "f2", "", true},
		{"the tree the Gate left", "f2", "f2", false},
		{"changed past what the Gate left", "f3", "f2", true},
		{"back to the tree the Gate saw", "f1", "f2", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newBuild(t).
				add(api.SessionObserved, api.SessionObservedPayload{SessionID: "s-1", Path: "/wt/a", Fingerprint: "f1"}).
				add(api.SessionChecked, api.SessionCheckedPayload{SessionID: "s-1", Fingerprint: "f1", AfterFingerprint: tt.after, Passed: true}).
				add(api.SessionObserved, api.SessionObservedPayload{SessionID: "s-1", Path: "/wt/a", Fingerprint: tt.current}).
				fold()
			if got := s.Sessions["s-1"].CheckStale(); got != tt.want {
				t.Errorf("CheckStale = %v, want %v", got, tt.want)
			}
		})
	}
}

// A check for a session never observed records nothing.
func TestSessionCheckedWithoutObservationIsIgnored(t *testing.T) {
	s := newBuild(t).add(api.SessionChecked, api.SessionCheckedPayload{SessionID: "s-9", Fingerprint: "f", Passed: true}).fold()
	if len(s.Sessions) != 0 {
		t.Errorf("Sessions = %v, want none", s.Sessions)
	}
}

// Sessions list in the order they were first seen.
func TestOrderedSessions(t *testing.T) {
	s := newBuild(t).
		add(api.SessionObserved, api.SessionObservedPayload{SessionID: "s-b", Path: "/wt/b"}).
		add(api.SessionObserved, api.SessionObservedPayload{SessionID: "s-a", Path: "/wt/a"}).
		add(api.SessionObserved, api.SessionObservedPayload{SessionID: "s-b", Path: "/wt/b", Fingerprint: "f2"}).
		fold()
	var got []string
	for _, x := range s.OrderedSessions() {
		got = append(got, x.ID)
	}
	if want := []string{"s-b", "s-a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func mustEvent(t *testing.T, typ api.EventType, payload any) api.Event {
	t.Helper()
	e, err := api.NewEvent(typ, "test", payload)
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	return e
}
