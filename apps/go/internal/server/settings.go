package server

import (
	"slices"
	"strings"
	"unicode"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

func ensureAppSettings(s *protocol.Snapshot) {
	if s.AppSettings.ProjectDirectory == "" {
		s.AppSettings.ProjectDirectory = "~"
	}
	if s.AppSettings.WorkspaceDefault == "" {
		s.AppSettings.WorkspaceDefault = "checkout"
	}
	if s.AppSettings.Revision == 0 {
		s.AppSettings.Revision = 1
	}
	for i := range s.Projects {
		if s.Projects[i].Revision == 0 {
			s.Projects[i].Revision = 1
		}
	}
	for _, capability := range []string{"app-settings", "project-settings", "restart-continuation", "path-completion", "workspace-file-context"} {
		if !slices.Contains(s.Capabilities, capability) {
			s.Capabilities = append(s.Capabilities, capability)
		}
	}
}

func applySettings(s *protocol.Snapshot, c protocol.Command) (string, error) {
	if c.Kind == "settings.update" {
		if c.Revision != s.AppSettings.Revision {
			return "", failure("stale_settings", "app settings changed; refresh before saving")
		}
		if c.AppSettings == nil || !slices.Contains([]string{"checkout", "worktree"}, c.AppSettings.WorkspaceDefault) {
			return "", failure("invalid", "choose current checkout or worktree")
		}
		next := *c.AppSettings
		if next.ProjectDirectory == "" {
			// Older clients omit this field when changing existing settings.
			// Preserve it; an explicit "~" resets the starting directory.
			next.ProjectDirectory = s.AppSettings.ProjectDirectory
			if next.ProjectDirectory == "" {
				next.ProjectDirectory = "~"
			}
		}
		if next.ProjectDirectory != s.AppSettings.ProjectDirectory {
			path, err := canonicalProjectPath(next.ProjectDirectory)
			if err != nil {
				return "", err
			}
			if next.ProjectDirectory != "~" {
				next.ProjectDirectory = path
			}
		}
		next.Revision = s.AppSettings.Revision + 1
		s.AppSettings = next
		return "", nil
	}
	for i := range s.Projects {
		p := &s.Projects[i]
		if p.ID != c.ProjectID {
			continue
		}
		if c.Revision != p.Revision {
			return "", failure("stale_project", "project or its threads changed; refresh before saving")
		}
		if c.Kind == "project.remove" {
			if reason := protocol.ProjectRemoveBlocked(*s, p.ID); reason != "" {
				return "", failure("project_busy", reason)
			}
			removed := map[string]bool{}
			threads := s.Threads[:0]
			for _, t := range s.Threads {
				if t.ProjectID == p.ID {
					removed[t.ID] = true
				} else {
					threads = append(threads, t)
				}
			}
			s.Threads = threads
			terminals := s.Terminals[:0]
			for _, terminal := range s.Terminals {
				if !removed[terminal.ThreadID] {
					terminals = append(terminals, terminal)
				}
			}
			s.Terminals = terminals
			s.Projects = append(s.Projects[:i], s.Projects[i+1:]...)
			return "", nil
		}
		if c.ProjectSettings == nil {
			return "", failure("invalid", "project settings are required")
		}
		value := *c.ProjectSettings
		value.Name = strings.TrimSpace(value.Name)
		if value.Name == "" || len(value.Name) > 256 || strings.ContainsFunc(value.Name, unicode.IsControl) {
			return "", failure("invalid", "project name must contain 1–256 bytes without control characters")
		}
		if !slices.Contains([]string{"", "folder", "code", "terminal", "git", "star", "rocket"}, value.Icon) || !slices.Contains([]string{"", "purple", "blue", "green", "orange", "pink", "teal"}, value.Color) {
			return "", failure("invalid", "unsupported project icon or color")
		}
		if !slices.Contains([]string{"", "checkout", "worktree"}, value.WorkspaceDefault) {
			return "", failure("invalid", "choose inherit, current checkout or worktree")
		}
		p.Name, p.Icon, p.Color, p.WorkspaceDefault = value.Name, value.Icon, value.Color, value.WorkspaceDefault
		p.Revision++
		for j := range s.Threads {
			if s.Threads[j].ProjectID == p.ID {
				s.Threads[j].Project = p.Name
			}
		}
		return p.ID, nil
	}
	return "", failure("not_found", "project does not exist")
}

// Only known fixture turns active at shutdown/crash qualify. Old interrupted
// snapshots lack the marker, and manual Stop clears it. Requests always require
// explicit revalidation and answers, even when continuation is enabled.
func restartEligible(t *protocol.Thread) bool {
	if t.Closed || t.Agent != "Fixture agent" || !validSettings(t.Effective) {
		return false
	}
	// A running thread can be between its completed turn and the next queued
	// dispatch. Preserve continuation across that durable tick boundary too.
	if fixtureTurnCompleted(t) && len(t.Queue) == 0 {
		return false
	}
	if !(t.State == "running" && !t.NeedsResume || t.State == "interrupted" && t.RestartEligible) {
		return false
	}
	for _, r := range t.Requests {
		if r.State == "pending" {
			return false
		}
	}
	for _, child := range t.Children {
		if child.State != "running" && child.State != "interrupted" && child.State != "completed" {
			return false
		}
	}
	return true
}

func recoverThreads(s *protocol.Snapshot) {
	for i := range s.Threads {
		t := &s.Threads[i]
		resume := s.AppSettings.ContinueAfterRestart && restartEligible(t)
		t.RestartEligible = false
		if t.State != "idle" {
			t.NeedsResume = true
			t.State = "interrupted"
		}
		for j := range t.Children {
			if t.Children[j].State == "running" {
				t.Children[j].State = "interrupted"
			}
		}
		for j := range t.Requests {
			if t.Requests[j].State == "pending" {
				t.Requests[j].Revision++
				t.Requests[j].Delivery = "revalidation-required"
			}
		}
		if resume {
			t.State, t.NeedsResume = "running", false
			for j := range t.Children {
				if t.Children[j].State == "interrupted" {
					t.Children[j].State = "running"
				}
			}
		}
	}
}
