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
			ID            int64  `json:"id"`
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
		RepoID:        payload.Repository.ID,
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
			Number       int    `json:"number"`
			Title        string `json:"title"`
			HTMLURL      string `json:"html_url"`
			DiffURL      string `json:"diff_url"`
			Body         string `json:"body"`
			Merged       bool   `json:"merged"`
			Draft        bool   `json:"draft"`
			Additions    int    `json:"additions"`
			Deletions    int    `json:"deletions"`
			ChangedFiles int    `json:"changed_files"`
			Commits      int    `json:"commits"`
			MergedAt     string `json:"merged_at"`
			ClosedAt     string `json:"closed_at"`
			User         struct {
				Login string `json:"login"`
			} `json:"user"`
			MergedBy *struct {
				Login string `json:"login"`
			} `json:"merged_by"`
			Head struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
			Labels []struct {
				Name string `json:"name"`
			} `json:"labels"`
			Assignees []struct {
				Login string `json:"login"`
			} `json:"assignees"`
			RequestedReviewers []struct {
				Login string `json:"login"`
			} `json:"requested_reviewers"`
		} `json:"pull_request"`
		Sender sender `json:"sender"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return notify.Event{}, true, err
	}
	pr := payload.PullRequest
	number := payload.Number
	if number == 0 {
		number = pr.Number
	}
	var mergedBy string
	if pr.MergedBy != nil {
		mergedBy = pr.MergedBy.Login
	}
	var labels []string
	for _, item := range pr.Labels {
		labels = append(labels, item.Name)
	}
	var assignees []string
	for _, item := range pr.Assignees {
		assignees = append(assignees, item.Login)
	}
	var reviewers []string
	for _, item := range pr.RequestedReviewers {
		reviewers = append(reviewers, item.Login)
	}
	return notify.Event{
		Type:          "pull_request",
		RepoID:        payload.Repository.ID,
		RepoFullName:  payload.Repository.FullName,
		DefaultBranch: payload.Repository.DefaultBranch,
		Actor:         payload.Sender.Login,
		Author:        pr.User.Login,
		Branch:        pr.Base.Ref,
		HeadBranch:    pr.Head.Ref,
		HeadSHA:       pr.Head.SHA,
		Title:         pr.Title,
		Summary:       payload.Action + " pull request",
		URL:           firstNonEmpty(pr.HTMLURL, payload.Repository.HTMLURL),
		DiffURL:       pr.DiffURL,
		Body:          pr.Body,
		Action:        payload.Action,
		Number:        number,
		Merged:        pr.Merged,
		IsDraft:       pr.Draft,
		Labels:        uniqueNames(labels),
		Assignees:     uniqueNames(assignees),
		Reviewers:     uniqueNames(reviewers),
		Additions:     pr.Additions,
		Deletions:     pr.Deletions,
		ChangedFiles:  pr.ChangedFiles,
		CommitCount:   pr.Commits,
		MergedBy:      mergedBy,
		MergedAt:      pr.MergedAt,
		ClosedAt:      pr.ClosedAt,
	}, true, nil
}

func uniqueNames(values []string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
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
		RepoID:        payload.Repository.ID,
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
	ID            int64  `json:"id"`
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
