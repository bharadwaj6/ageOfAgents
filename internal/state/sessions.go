package state

import (
	"sort"
	"time"

	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// Session is an agent session aoa did not start: a git worktree seen by
// `aoa sessions` (ADR 022). It is the fold of that worktree's SessionObserved
// and SessionChecked events — what git showed of it, and whether the Gate
// passed on it.
type Session struct {
	ID          string
	Path        string
	Branch      string
	Head        string
	Base        string
	Files       []string
	TestFiles   []string
	Dirty       bool
	Fingerprint string
	LastChange  time.Time
	FirstSeen   time.Time
	LastSeen    time.Time
	FirstSeq    int
	Removed     bool
	Check       *SessionCheck // the latest Gate run on it; nil if never checked
}

// SessionCheck is the latest Gate run on a session's working tree.
type SessionCheck struct {
	Fingerprint string
	Passed      bool
	Command     string
	Output      string
	At          time.Time
}

// CheckStale reports whether the session's tree has changed since its latest
// check, so the check's verdict no longer describes it. A session never
// checked is not stale — it has no verdict to be stale.
func (x *Session) CheckStale() bool {
	return x.Check != nil && x.Check.Fingerprint != x.Fingerprint
}

// Same reports whether an observation would record nothing new about this
// session: the same content, branch and base, still present.
func (x *Session) Same(p api.SessionObservedPayload) bool {
	return !x.Removed && !p.Removed && x.Fingerprint == p.Fingerprint &&
		x.Branch == p.Branch && x.Base == p.Base && x.Path == p.Path
}

// OrderedSessions returns every session in the order it was first seen.
func (s *State) OrderedSessions() []*Session {
	out := make([]*Session, 0, len(s.Sessions))
	for _, x := range s.Sessions {
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FirstSeq != out[j].FirstSeq {
			return out[i].FirstSeq < out[j].FirstSeq
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (s *State) applySessionObserved(e api.Event) error {
	var p api.SessionObservedPayload
	if err := e.DecodePayload(&p); err != nil {
		return err
	}
	if s.Sessions == nil {
		s.Sessions = map[string]*Session{}
	}
	x := s.Sessions[p.SessionID]
	if x == nil {
		x = &Session{ID: p.SessionID, FirstSeen: e.Timestamp, FirstSeq: e.Seq}
		s.Sessions[p.SessionID] = x
	}
	x.Path = p.Path
	x.LastSeen = e.Timestamp
	if p.Removed {
		// Keep what the session last showed: the point of the ledger is
		// knowing what a session did after its worktree is cleaned up.
		x.Removed = true
		return nil
	}
	x.Removed = false
	x.Branch, x.Head, x.Base = p.Branch, p.Head, p.Base
	x.Files, x.TestFiles = p.Files, p.TestFiles
	x.Dirty, x.Fingerprint, x.LastChange = p.Dirty, p.Fingerprint, p.LastChange
	return nil
}

func (s *State) applySessionChecked(e api.Event) error {
	var p api.SessionCheckedPayload
	if err := e.DecodePayload(&p); err != nil {
		return err
	}
	x := s.Sessions[p.SessionID]
	if x == nil {
		// A check always follows an observation of the same session; one
		// without it is a log from somewhere else. Record nothing rather
		// than invent a session with no path.
		return nil
	}
	x.Check = &SessionCheck{
		Fingerprint: p.Fingerprint, Passed: p.Passed,
		Command: p.Command, Output: p.Output, At: e.Timestamp,
	}
	return nil
}
