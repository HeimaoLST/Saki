package main

import (
	"strings"
	"unicode/utf8"
)

type contactHistoryMatch struct {
	SessionID string
	EntryID   string
	Date      string
	From      string
	Preview   string
}

type contactHistoryFailure struct {
	SessionID string
	Err       error
}

type contactHistoryResult struct {
	Matches  []contactHistoryMatch
	Failures []contactHistoryFailure
	Limited  bool
}

const contactHistoryMatchLimit = 100

// Session IDs are an explicit contact-owned scope. Search never discovers
// unrelated sessions or loads them for execution; EntryID locates the full
// message through the same history projection used by the conversation view.
func (a *appModel) searchContactHistory(rt *runtimeClient, sessionIDs []string, query string) contactHistoryResult {
	query = strings.ToLower(strings.TrimSpace(query))
	result := contactHistoryResult{}
	seen := make(map[string]bool, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		if sessionID == "" || seen[sessionID] {
			continue
		}
		seen[sessionID] = true
		records, _, err := a.historyAllFor(rt, sessionID)
		if err != nil {
			result.Failures = append(result.Failures, contactHistoryFailure{SessionID: sessionID, Err: err})
			continue
		}
		entries := parseHistory(sessionID, records, nil).entries
		for i := len(entries) - 1; i >= 0; i-- {
			entry := entries[i]
			if !strings.Contains(strings.ToLower(entry.Text), query) {
				continue
			}
			result.Matches = append(result.Matches, contactHistoryMatch{
				SessionID: sessionID, EntryID: entry.ID, Date: entry.Date, From: entry.From,
				Preview: contactHistoryPreview(entry.Text, query),
			})
			if len(result.Matches) == contactHistoryMatchLimit {
				result.Limited = true
				return result
			}
		}
	}
	return result
}

func contactHistoryPreview(text, query string) string {
	const length = 240
	runes := []rune(text)
	if len(runes) <= length {
		return text
	}
	start := 0
	lower := strings.ToLower(text)
	if index := strings.Index(lower, query); index >= 0 {
		start = max(0, utf8.RuneCountInString(lower[:index])-60)
	}
	end := min(len(runes), start+length)
	preview := string(runes[start:end])
	if start > 0 {
		preview = "…" + preview
	}
	if end < len(runes) {
		preview += "…"
	}
	return preview
}
