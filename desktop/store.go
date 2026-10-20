package main

// GUI-owned contact store. It persists only product fields and the mapping to
// a runtime session_id; messages/history stay in the runtime and are read via
// session.history. The JSON file is private to the GUI.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const contactStoreVersion = 1

type storedContact struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Role         string `json:"role,omitempty"`
	Purpose      string `json:"purpose,omitempty"`
	Instructions string `json:"instructions,omitempty"`
	Workspace    string `json:"workspace,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	Draft        string `json:"draft,omitempty"`
	UpdatedAt    int64  `json:"updated_at,omitempty"`
}

type storedState struct {
	Version  int             `json:"version"`
	Selected string          `json:"selected,omitempty"`
	Contacts []storedContact `json:"contacts"`
}

type contactStore struct {
	path  string
	state storedState
}

func newContactID() string {
	var bytes [6]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return "contact-" + hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("contact-%d", time.Now().UnixNano())
}

func nowMillis() int64 { return time.Now().UnixMilli() }

// openContactStore loads the store, or creates and persists a default Saki
// contact when the file does not exist. A corrupt or unsupported file is
// reported and left untouched; the app must not silently replace it.
func openContactStore(path, defaultWorkspace string) (*contactStore, error) {
	store := &contactStore{path: path}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &store.state); err != nil {
			return nil, fmt.Errorf("contact store %s is not readable: %w", path, err)
		}
		if store.state.Version != contactStoreVersion {
			return nil, fmt.Errorf("contact store %s has unsupported version %d", path, store.state.Version)
		}
		if len(store.state.Contacts) == 0 {
			return nil, fmt.Errorf("contact store %s has no contacts", path)
		}
		seen := map[string]bool{}
		for _, contact := range store.state.Contacts {
			if contact.ID == "" || seen[contact.ID] {
				return nil, fmt.Errorf("contact store %s has an empty or duplicate contact id", path)
			}
			seen[contact.ID] = true
		}
		if !seen[store.state.Selected] {
			store.state.Selected = store.state.Contacts[0].ID
		}
		return store, nil
	case errors.Is(err, os.ErrNotExist):
		store.state = storedState{
			Version: contactStoreVersion,
			Contacts: []storedContact{{
				ID:        newContactID(),
				Name:      "Saki",
				Role:      "Assistant",
				Workspace: defaultWorkspace,
				UpdatedAt: nowMillis(),
			}},
		}
		store.state.Selected = store.state.Contacts[0].ID
		if err := store.saveState(); err != nil {
			return nil, err
		}
		return store, nil
	default:
		return nil, fmt.Errorf("contact store %s is not accessible: %w", path, err)
	}
}

func (s *contactStore) saveState() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create contact store directory: %w", err)
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(dir, ".contacts-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, s.path)
}

// save writes the current in-memory contacts. It is called from the change
// event (drafts) and from explicit product edits; it does not rewrite messages.
func (s *contactStore) save(contacts []*contact, selected string) error {
	s.state = storedState{
		Version:  contactStoreVersion,
		Selected: selected,
		Contacts: make([]storedContact, 0, len(contacts)),
	}
	for _, c := range contacts {
		s.state.Contacts = append(s.state.Contacts, storedContact{
			ID:           c.ID,
			Name:         c.Name,
			Role:         c.Role,
			Purpose:      c.Purpose,
			Instructions: c.Instructions,
			Workspace:    c.Workspace,
			SessionID:    c.SessionID,
			Draft:        c.Draft,
			UpdatedAt:    nowMillis(),
		})
	}
	if !s.hasContact(selected) && len(contacts) > 0 {
		s.state.Selected = contacts[0].ID
	}
	return s.saveState()
}

func (s *contactStore) hasContact(id string) bool {
	for _, c := range s.state.Contacts {
		if c.ID == id {
			return true
		}
	}
	return false
}
