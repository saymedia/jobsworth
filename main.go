package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"

	"gopkg.in/libgit2/git2go.v22"
	"gopkg.in/yaml.v2"
)

var rollbackMessageRegexp = regexp.MustCompile("^[Rr]oll\\s*back\\s+(to\\s+)?#?(\\d+)")
var envOverrideMessageRegexp = regexp.MustCompile("^[Dd]eploy\\s*(#?(\\d+)\\s*)?(to\\s+)?(\\S+)")

const bkWait = "wait"

func main() {
	var err error
	flag.Parse()

	args := flag.Args()

	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "Usage: jobsworth <pipeline-file>\n\n")
		os.Exit(1)
	}

	if os.Getenv("BUILDKITE") != "true" {
		fmt.Fprintf(
			os.Stderr, "This tool is intended to run within a BuildKite job\n\n",
		)
		os.Exit(1)
	}

	context := &Context{
		ConfigFilename:            args[0],
		BranchName:                os.Getenv("BUILDKITE_BRANCH"),
		BuildMessage:              os.Getenv("BUILDKITE_MESSAGE"),
		RepoURL:                   os.Getenv("BUILDKITE_REPO"),
		BuildEnvironment:          os.Getenv("JOBSWORTH_ENVIRONMENT"),
		BuildkiteJobId:            os.Getenv("BUILDKITE_JOB_ID"),
		BuildkiteBuildId:          os.Getenv("BUILDKITE_JOB_ID"),
		BuildkiteAgentAccessToken: os.Getenv("BUILDKITE_AGENT_ACCESS_TOKEN"),
		BuildkiteAgentEndpointURL: os.Getenv("BUILDKITE_AGENT_ENDPOINT"),
	}
	if os.Getenv("BUILDKITE_PULL_REQUEST") != "false" {
		context.InPullRequest = true
	}
	context.BuildNumber, err = strconv.ParseUint(
		os.Getenv("BUILDKITE_BUILD_NUMBER"), 10, 64,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "BUILDKITE_BUILD_NUMBER invalid: %s", err)
		os.Exit(1)
	}

	if context.BuildEnvironment == "" {
		fmt.Fprintf(os.Stderr, "JOBSWORTH_ENVIRONMENT environment variable not set\n")
		os.Exit(1)
	}

	context.CodeVersion, err = buildCodeVersion(context)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error forming code version: %s", err)
		os.Exit(1)
	}

	// Certain micro-syntaxes in the build message trigger special behaviors,
	// like rolling back to an earlier artifact.
	context.DoMessageMagic()

	err = run(context)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(2)
	}
}

func run(context *Context) error {
	pipeline, err := LoadPipelineFromFile(context.ConfigFilename)
	if err != nil {
		return fmt.Errorf("Error parsing pipeline: %s", err)
	}

	bkSteps, err := pipeline.Lower(context)
	if err != nil {
		return fmt.Errorf("Error lowering pipeline: %s", err)
	}

	outputBytes, err := yaml.Marshal(map[string]interface{}{
		"steps": bkSteps,
	})
	os.Stdout.Write(outputBytes)

	return nil
}

func buildCodeVersion(context *Context) (string, error) {
	repo, err := git.OpenRepository(".")
	if err != nil {
		return "", err
	}

	head, err := repo.Head()
	if err != nil {
		return "", err
	}

	// Drill down to an actual commit
	commitObj, err := head.Peel(git.ObjectCommit)
	if err != nil {
		return "", err
	}

	commit := commitObj.(*git.Commit)

	commitId := commit.Id()
	commitTime := commit.Committer().When.UTC()

	return fmt.Sprintf(
		"%s-%s-%06d",
		commitTime.Format("2006-01-02-150405"),
		commitId.String()[:7],
		context.BuildNumber,
	), nil
}
