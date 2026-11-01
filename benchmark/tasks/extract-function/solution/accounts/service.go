// Package accounts registers users and invitations.
package accounts

import (
	"errors"
	"fmt"
	"strings"
)

// Store persists accounts.
type Store interface {
	CreateUser(email, name string) error
	CreateInvite(email, invitedBy string) error
	Exists(email string) bool
}

// SignupRequest is a self-service registration.
type SignupRequest struct {
	Email string
	Name  string
}

// InviteRequest is an invitation sent by an existing user.
type InviteRequest struct {
	Email     string
	InvitedBy string
}

var blockedDomains = []string{"mailinator.com", "example.invalid"}

// Service implements the account use cases.
type Service struct {
	Store Store
}

// normalizeEmail returns the trimmed, lower-cased address or why it is invalid.
func normalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" {
		return "", errors.New("email is required")
	}
	if len(email) > 254 {
		return "", errors.New("email is too long")
	}
	at := strings.LastIndex(email, "@")
	if at < 1 || at == len(email)-1 {
		return "", errors.New("email is missing @")
	}
	domain := email[at+1:]
	if !strings.Contains(domain, ".") {
		return "", fmt.Errorf("email domain %q has no dot", domain)
	}
	for _, b := range blockedDomains {
		if domain == b {
			return "", fmt.Errorf("email domain %q is blocked", domain)
		}
	}
	return email, nil
}

// ProcessSignup validates and stores a self-service registration.
func (s *Service) ProcessSignup(req SignupRequest) error {
	email, err := normalizeEmail(req.Email)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return errors.New("name is required")
	}
	if s.Store.Exists(email) {
		return fmt.Errorf("account %s already exists", email)
	}
	return s.Store.CreateUser(email, name)
}

// ProcessInvite validates and stores an invitation.
func (s *Service) ProcessInvite(req InviteRequest) error {
	if req.InvitedBy == "" {
		return errors.New("inviter is required")
	}
	email, err := normalizeEmail(req.Email)
	if err != nil {
		return err
	}
	if s.Store.Exists(email) {
		return fmt.Errorf("%s is already a member", email)
	}
	return s.Store.CreateInvite(email, req.InvitedBy)
}
