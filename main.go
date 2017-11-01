package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/libgit2/git2go.v24"
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
		BuildkiteBuildId:          os.Getenv("BUILDKITE_BUILD_ID"),
		BuildkiteAgentAccessToken: os.Getenv("BUILDKITE_AGENT_ACCESS_TOKEN"),
		BuildkiteAgentEndpointURL: os.Getenv("BUILDKITE_AGENT_ENDPOINT"),
		BuildkiteAPIAccessToken:   os.Getenv("JOBSWORTH_BUILDKITE_API_TOKEN"),
		BuildkitePipelineSlug:     os.Getenv("BUILDKITE_PIPELINE_SLUG"),
		BuildkiteOrganizationSlug: os.Getenv("BUILDKITE_ORGANIZATION_SLUG"),
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
	if context.BuildkiteAPIAccessToken == "" {
		fmt.Fprintf(os.Stderr, "JOBSWORTH_BUILDKITE_API_TOKEN environment variable not set\n")
		os.Exit(1)
	}

	gitCommit, err := getCurrentGitCommit()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading current git commit: %s", err)
		os.Exit(1)
	}
	context.SetGitCommit(gitCommit)

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

	buildkite := context.Buildkite()

	writeMetadata := map[string]string{}
	if context.ArtifactsFromBuildNumber != "" {
		fmt.Printf(
			"Re-using artifacts from build #%s\n",
			context.ArtifactsFromBuildNumber,
		)
		// Copy all the non-deployment-related metadata from
		// the given build number.
		// When ArtifactsFromBuildNumber is set, pipeline.Lower
		// will skip the smoke test and build steps under the
		// assumption that all of the relevant metadata would've
		// been copied from the original job.
		otherMeta, err := buildkite.ReadOtherBuildMetadata(context.ArtifactsFromBuildNumber)
		if err != nil {
			return fmt.Errorf(
				"error reading job #%s metadata: %s",
				context.ArtifactsFromBuildNumber, err,
			)
		}

		codeVersion := otherMeta["jobsworth:code_version"]
		if codeVersion == "" {
			return fmt.Errorf(
				"build #%s does not have a recorded code version",
				context.ArtifactsFromBuildNumber,
			)
		}
		sourceCommitId := otherMeta["jobsworth:source_commit_id"]
		if sourceCommitId == "" {
			return fmt.Errorf(
				"build #%s does not have a recorded source commit id",
				context.ArtifactsFromBuildNumber,
			)
		}

		// The other job's code version and commit override what
		// we detected from the current context.
		context.CodeVersion = codeVersion
		context.SourceGitCommitId = sourceCommitId

		for k, v := range otherMeta {
			// build: is the expected convention for significant
			// metadata created during the build phase.
			// We also support "artifact_" for now, but it's deprecated.
			if strings.HasPrefix(k, "build:") || strings.HasPrefix(k, "artifact_") {
				writeMetadata[k] = v
			}
		}
	}

	if context.OverrideDeployEnvironmentName != "" {
		fmt.Printf(
			"Forcing deployment to non-standard environment %s\n",
			context.OverrideDeployEnvironmentName,
		)
	}

	bkSteps, err := pipeline.Lower(context)
	if err != nil {
		return fmt.Errorf("Error lowering pipeline: %s", err)
	}

	writeMetadata["jobsworth:code_version"] = context.CodeVersion
	writeMetadata["jobsworth:source_commit_id"] = context.SourceGitCommitId
	err = buildkite.WriteJobMetadata(writeMetadata)
	if err != nil {
		return fmt.Errorf("error writing job metadata: %s", err)
	}

	err = buildkite.InsertPipelineSteps(bkSteps)
	if err != nil {
		return fmt.Errorf("error inserting new pipeline steps: %s", err)
	}

	return nil
}

func getCurrentGitCommit() (*git.Commit, error) {
	repo, err := git.OpenRepository(".")
	if err != nil {
		return nil, err
	}

	head, err := repo.Head()
	if err != nil {
		return nil, err
	}

	// Drill down to an actual commit
	commitObj, err := head.Peel(git.ObjectCommit)
	if err != nil {
		return nil, err
	}

	return commitObj.(*git.Commit), nil
}
