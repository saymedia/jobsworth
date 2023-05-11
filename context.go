package main

import (
	"fmt"
	"strings"

	"github.com/libgit2/git2go/v34"
)

type Context struct {
	BuildNumber                   uint64
	BuildkitePipelineSlug         string
	BuildkiteOrganizationSlug     string
	BuildkiteAgentAccessToken     string
	BuildkiteAgentEndpointURL     string
	BuildkiteAPIAccessToken       string
	BuildkiteJobId                string
	BuildkiteBuildId              string
	ConfigFilename                string
	BranchName                    string
	BuildMessage                  string
	RepoURL                       string
	InPullRequest                 bool
	BuildEnvironment              string
	CodeVersion                   string
	SourceGitCommitId             string
	ArtifactsFromBuildNumber      string
	OverrideDeployEnvironmentName string
}

type StepContext struct {
	EnvironmentName string
	QueueName       string
	EmojiName       string
	Cautious        bool
	// use concurrency and concurrency_group to force only one to run at a time
	PreventConcurrency bool
}

// CodebaseName tries to infer a name for the codebase from the repository
// URL.
//
// It does this by extracting the final slash-separated portion of the URL,
// and removing a .git suffix if present.
//
// For example, git@github.com:example/foo.git would return "foo".
func (c *Context) CodebaseName() string {
	repoUrl := c.RepoURL

	slashIndex := strings.LastIndex(repoUrl, "/")
	lastPart := repoUrl[slashIndex+1:]

	if strings.HasSuffix(lastPart, ".git") {
		lastPart = lastPart[:len(lastPart)-4]
	}

	return lastPart
}

func (c *Context) DoMessageMagic() {
	{
		matchParts := rollbackMessageRegexp.FindStringSubmatch(c.BuildMessage)
		if len(matchParts) == 3 {
			c.ArtifactsFromBuildNumber = matchParts[2]
			return
		}
	}

	{
		matchParts := envOverrideMessageRegexp.FindStringSubmatch(c.BuildMessage)
		if len(matchParts) == 5 {
			c.ArtifactsFromBuildNumber = matchParts[2]
			c.OverrideDeployEnvironmentName = matchParts[4]
			return
		}
	}
}

func (c *Context) SetGitCommit(commit *git.Commit) {
	commitId := commit.Id()
	commitTime := commit.Committer().When.UTC()

	c.CodeVersion = fmt.Sprintf(
		"%s-%s-%06d",
		commitTime.Format("2006-01-02-150405"),
		commitId.String()[:7],
		c.BuildNumber,
	)
	c.SourceGitCommitId = commitId.String()
}

func (c *StepContext) CautiousStr() string {
	if c.Cautious {
		return "1"
	} else {
		return "0"
	}
}
