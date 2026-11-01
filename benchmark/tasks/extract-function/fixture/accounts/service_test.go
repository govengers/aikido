package accounts

import "testing"

type memStore struct {
	users   map[string]string
	invites map[string]string
}

func newMemStore() *memStore {
	return &memStore{users: map[string]string{}, invites: map[string]string{}}
}

func (m *memStore) CreateUser(email, name string) error { m.users[email] = name; return nil }
func (m *memStore) CreateInvite(email, by string) error { m.invites[email] = by; return nil }
func (m *memStore) Exists(email string) bool            { _, ok := m.users[email]; return ok }

func TestProcessSignup(t *testing.T) {
	st := newMemStore()
	s := &Service{Store: st}
	if err := s.ProcessSignup(SignupRequest{Email: "  Ann@Example.COM ", Name: "Ann"}); err != nil {
		t.Fatal(err)
	}
	if st.users["ann@example.com"] != "Ann" {
		t.Fatalf("users = %v", st.users)
	}
	if err := s.ProcessSignup(SignupRequest{Email: "ann@example.com", Name: "Ann"}); err == nil {
		t.Fatal("duplicate accepted")
	}
	for _, bad := range []string{"", "nobody", "x@localhost", "x@mailinator.com", "@example.com"} {
		if err := s.ProcessSignup(SignupRequest{Email: bad, Name: "X"}); err == nil {
			t.Errorf("signup with %q accepted", bad)
		}
	}
}

func TestProcessInvite(t *testing.T) {
	st := newMemStore()
	s := &Service{Store: st}
	if err := s.ProcessInvite(InviteRequest{Email: "Bob@Example.org", InvitedBy: "ann"}); err != nil {
		t.Fatal(err)
	}
	if st.invites["bob@example.org"] != "ann" {
		t.Fatalf("invites = %v", st.invites)
	}
	if err := s.ProcessInvite(InviteRequest{Email: "bob@example.org"}); err == nil || err.Error() != "inviter is required" {
		t.Fatalf("missing inviter: %v", err)
	}
	if err := s.ProcessInvite(InviteRequest{Email: "bob@", InvitedBy: "ann"}); err == nil || err.Error() != "email is missing @" {
		t.Fatalf("bad email: %v", err)
	}
}
