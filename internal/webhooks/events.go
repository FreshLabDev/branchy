// SPDX-License-Identifier: Apache-2.0
package webhooks

import (
	"encoding/json"
	"fmt"
	"strings"

	"branchy/internal/notify"
)

type SubscriptionFilter struct {
	BranchMode         string
	BranchNames        []string
	DefaultBranch      string
	PullRequestActions []string
	ReleaseMode        string
}

func ParseEvent(eventName string, body []byte) (notify.Event, bool, error) {
	switch eventName {
	case "push":
		return parsePush(body)
	case "pull_request":
		return parsePullRequest(body)
	case "release":
		return parseRelease(body)
	default:
		return notify.Event{}, false, nil
	}
}

func MatchesBranch(filter SubscriptionFilter, event notify.Event) bool {
	switch filter.BranchMode {
	case "all", "":
		return true
	case "default":
		defaultBranch := filter.DefaultBranch
		if defaultBranch == "" {
			defaultBranch = event.DefaultBranch
		}
		return event.Branch == defaultBranch
	case "selected":
		for _, branch := range filter.BranchNames {
			if event.Branch == branch {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func MatchesSubscription(filter SubscriptionFilter, event notify.Event) bool {
	switch event.Type {
	case "push":
		return MatchesBranch(filter, event)
	case "pull_request":
		return MatchesBranch(filter, event) && MatchesPullRequestAction(filter.PullRequestActions, event)
	case "release":
		return MatchesReleaseMode(filter.ReleaseMode, event)
	default:
		return false
	}
}

func MatchesPullRequestAction(actions []string, event notify.Event) bool {
	allowed := normalizePullRequestActions(actions)
	for _, action := range allowed {
		switch action {
		case "opened":
			if event.Action == "opened" || event.Action == "reopened" {
				return true
			}
		case "merged":
			if event.Merged {
				return true
			}
		case "closed":
			if event.Action == "closed" && !event.Merged {
				return true
			}
		}
	}
	return false
}

func MatchesReleaseMode(mode string, event notify.Event) bool {
	switch mode {
	case "", "all":
		return true
	case "releases":
		return !event.Prerelease
	case "prereleases":
		return event.Prerelease
	default:
		return false
	}
}

func parsePush(body []byte) (notify.Event, bool, error) {
	var payload struct {
		Ref        string `json:"ref"`
		Before     string `json:"before"`
		After      string `json:"after"`
		Compare    string `json:"compare"`
		Deleted    bool   `json:"deleted"`
		Repository struct {
			FullName      string `json:"full_name"`
			DefaultBranch string `json:"default_branch"`
			HTMLURL       string `json:"html_url"`
		} `json:"repository"`
		Pusher struct {
			Name string `json:"name"`
		} `json:"pusher"`
		Sender struct {
			Login string `json:"login"`
		} `json:"sender"`
		HeadCommit *struct {
			Message string `json:"message"`
			URL     string `json:"url"`
		} `json:"head_commit"`
		Commits []struct {
			ID      string `json:"id"`
			Message string `json:"message"`
			URL     string `json:"url"`
			Author  struct {
				Name     string `json:"name"`
				Username string `json:"username"`
			} `json:"author"`
			Committer struct {
				Name     string `json:"name"`
				Username string `json:"username"`
			} `json:"committer"`
		} `json:"commits"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return notify.Event{}, true, err
	}
	if payload.Deleted || !strings.HasPrefix(payload.Ref, "refs/heads/") || len(payload.Commits) == 0 {
		return notify.Event{}, false, nil
	}
	branch := strings.TrimPrefix(payload.Ref, "refs/heads/")
	actor := firstNonEmpty(payload.Sender.Login, payload.Pusher.Name)
	commits := make([]notify.Commit, 0, len(payload.Commits))
	for _, commit := range payload.Commits {
		commits = append(commits, notify.Commit{
			SHA:     commit.ID,
			Message: firstLine(commit.Message),
			URL:     commit.URL,
			Author:  firstNonEmpty(commit.Author.Username, commit.Author.Name, commit.Committer.Username, commit.Committer.Name, actor),
		})
	}
	return notify.Event{
		Type:          "push",
		RepoFullName:  payload.Repository.FullName,
		DefaultBranch: payload.Repository.DefaultBranch,
		Actor:         actor,
		Branch:        branch,
		Title:         fmt.Sprintf("%d new commit(s)", len(payload.Commits)),
		Summary:       fmt.Sprintf("Commits to %s", branch),
		URL:           firstNonEmpty(payload.Compare, payload.Repository.HTMLURL),
		CompareURL:    payload.Compare,
		Commits:       commits,
		CommitCount:   len(payload.Commits),
	}, true, nil
}

func parsePullRequest(body []byte) (notify.Event, bool, error) {
	var payload struct {
		Action      string `json:"action"`
		Number      int    `json:"number"`
		Repository  repo   `json:"repository"`
		PullRequest struct {
			Title   string `json:"title"`
			HTMLURL string `json:"html_url"`
			Body    string `json:"body"`
			Merged  bool   `json:"merged"`
			Base    struct {
				Ref string `json:"ref"`
			} `json:"base"`
		} `json:"pull_request"`
		Sender sender `json:"sender"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return notify.Event{}, true, err
	}
	return notify.Event{
		Type:          "pull_request",
		RepoFullName:  payload.Repository.FullName,
		DefaultBranch: payload.Repository.DefaultBranch,
		Actor:         payload.Sender.Login,
		Branch:        payload.PullRequest.Base.Ref,
		Title:         payload.PullRequest.Title,
		Summary:       payload.Action + " pull request",
		URL:           firstNonEmpty(payload.PullRequest.HTMLURL, payload.Repository.HTMLURL),
		Body:          payload.PullRequest.Body,
		Action:        payload.Action,
		Number:        payload.Number,
		Merged:        payload.PullRequest.Merged,
	}, true, nil
}

func parseRelease(body []byte) (notify.Event, bool, error) {
	var payload struct {
		Action     string `json:"action"`
		Repository repo   `json:"repository"`
		Release    struct {
			Name            string `json:"name"`
			TagName         string `json:"tag_name"`
			TargetCommitish string `json:"target_commitish"`
			HTMLURL         string `json:"html_url"`
			Body            string `json:"body"`
			Prerelease      bool   `json:"prerelease"`
		} `json:"release"`
		Sender sender `json:"sender"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return notify.Event{}, true, err
	}
	if payload.Action != "published" {
		return notify.Event{}, false, nil
	}
	title := firstNonEmpty(payload.Release.Name, payload.Release.TagName)
	return notify.Event{
		Type:          "release",
		RepoFullName:  payload.Repository.FullName,
		DefaultBranch: payload.Repository.DefaultBranch,
		Actor:         payload.Sender.Login,
		Branch:        payload.Release.TargetCommitish,
		Title:         title,
		Summary:       payload.Action + " release",
		URL:           firstNonEmpty(payload.Release.HTMLURL, payload.Repository.HTMLURL),
		Body:          payload.Release.Body,
		Action:        payload.Action,
		TagName:       payload.Release.TagName,
		Prerelease:    payload.Release.Prerelease,
	}, true, nil
}

type repo struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
}

type sender struct {
	Login string `json:"login"`
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if idx := strings.IndexByte(value, '\n'); idx >= 0 {
		return value[:idx]
	}
	return value
}

func normalizePullRequestActions(actions []string) []string {
	if len(actions) == 0 {
		return []string{"opened", "merged", "closed"}
	}
	seen := make(map[string]bool)
	for _, action := range actions {
		seen[strings.TrimSpace(action)] = true
	}
	var out []string
	for _, action := range []string{"opened", "merged", "closed"} {
		if seen[action] {
			out = append(out, action)
		}
	}
	return out
}
