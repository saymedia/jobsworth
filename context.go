package main

import (
	"strings"
)

type Context struct {
	BuildNumber                   uint64
	BuildkiteAgentAccessToken     string
	BuildkiteAgentEndpointURL     string
	BuildkiteJobId                string
	BuildkiteBuildId              string
	ConfigFilename                string
	BranchName                    string
	BuildMessage                  string
	RepoURL                       string
	InPullRequest                 bool
	BuildEnvironment              string
	CodeVersion                   string
	ArtifactsFromBuildNumber      string
	OverrideDeployEnvironmentName string
}

type StepContext struct {
	EnvironmentName string
	QueueName       string
	EmojiName       string
	Cautious        bool
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

func (c *StepContext) CautiousStr() string {
	if c.Cautious {
		return "1"
	} else {
		return "0"
	}
}
