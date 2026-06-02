// SPDX-License-Identifier: Apache-2.0
package webhooks

import (
	"encoding/json"
	"fmt"
	"strings"

	"branchy/internal/notify"
)

type SubscriptionFilter struct {
	BranchMode    string
	BranchName    string
	DefaultBranch string
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
		return event.Branch == filter.BranchName
	default:
		return false
	}
}

func parsePush(body []byte) (notify.Event, bool, error) {
	var payload struct {
		Ref        string `json:"ref"`
		Compare    string `json:"compare"`
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
		Commits []struct{} `json:"commits"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return notify.Event{}, true, err
	}
	branch := strings.TrimPrefix(payload.Ref, "refs/heads/")
	actor := firstNonEmpty(payload.Sender.Login, payload.Pusher.Name)
	title := fmt.Sprintf("%d commit(s) pushed", len(payload.Commits))
	url := firstNonEmpty(payload.Compare, payload.Repository.HTMLURL)
	if payload.HeadCommit != nil {
		title = firstLine(payload.HeadCommit.Message)
		url = firstNonEmpty(payload.HeadCommit.URL, url)
	}
	return notify.Event{
		Type:          "push",
		RepoFullName:  payload.Repository.FullName,
		DefaultBranch: payload.Repository.DefaultBranch,
		Actor:         actor,
		Branch:        branch,
		Title:         title,
		Summary:       fmt.Sprintf("Push to %s", branch),
		URL:           url,
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
		Title:         fmt.Sprintf("#%d %s", payload.Number, payload.PullRequest.Title),
		Summary:       payload.Action + " pull request",
		URL:           firstNonEmpty(payload.PullRequest.HTMLURL, payload.Repository.HTMLURL),
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
		} `json:"release"`
		Sender sender `json:"sender"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return notify.Event{}, true, err
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
