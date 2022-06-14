package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/libgit2/git2go.v26"
	"gopkg.in/yaml.v2"
)

var rollbackMessageRegexp = regexp.MustCompile("^[Rr]oll\\s*back\\s+(to\\s+)?#?(\\d+)")
var envOverrideMessageRegexp = regexp.MustCompile("^[Dd]eploy\\s*(#?(\\d+)\\s*)?(to\\s+)?(\\S+)")

// variables that will be set at link time (see .goreleaser.yaml)
var version string = "development"
var commit string = ""
var date string = ""
var builtBy string = ""

const bkWait = "wait"

func main() {
	var err error
	dryRun := flag.Bool("dry-run", false, "print the steps and metadata instead of uploading to BuildKite")
	versionFlag := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	args := flag.Args()

	if *versionFlag {
		fmt.Printf("jobsworth version %s (%s)\n", version, commit)
		fmt.Printf("date: %s\n", date)
		fmt.Printf("built by: %s\n", builtBy)
		os.Exit(0)
	}

	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "Usage: jobsworth <pipeline-file>\n\n")
		os.Exit(1)
	}

	if !*dryRun && os.Getenv("BUILDKITE") != "true" {
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
	if buildNumberString := os.Getenv("BUILDKITE_BUILD_NUMBER"); buildNumberString != "" {
		context.BuildNumber, err = strconv.ParseUint(
			buildNumberString, 10, 64,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "BUILDKITE_BUILD_NUMBER invalid: %s\n", err)
			os.Exit(1)
		}
	} else if !*dryRun {
		fmt.Fprintf(os.Stderr, "BUILDKITE_BUILD_NUMBER not set: %s\n", err)
		os.Exit(1)
	}

	if context.BuildEnvironment == "" {
		if *dryRun {
			context.BuildEnvironment = "dry-run-default"
		} else {
			fmt.Fprintf(os.Stderr, "JOBSWORTH_ENVIRONMENT environment variable not set\n")
			os.Exit(1)
		}
	}
	if context.BuildkiteAPIAccessToken == "" {
		if *dryRun {
			context.BuildkiteAPIAccessToken = "dry-run-default"
		} else {
			fmt.Fprintf(os.Stderr, "JOBSWORTH_BUILDKITE_API_TOKEN environment variable not set\n")
			os.Exit(1)
		}
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

	err = run(context, *dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(2)
	}
}

func run(context *Context, dryRun bool) error {
	if dryRun {
		buildkite := DryRunBuildMetadataClient{}
		bkSteps, writeMetadata, err := generateSteps(context, &buildkite)
		if err != nil {
			return err
		}
		return printSteps(bkSteps, writeMetadata)
	} else {
		buildkite := context.Buildkite()
		bkSteps, writeMetadata, err := generateSteps(context, buildkite)
		if err != nil {
			return err
		}
		return uploadSteps(context, buildkite, bkSteps, writeMetadata)
	}
}

func generateSteps(context *Context, buildkite BuildMetadataClient) (
	[]interface{}, map[string]string, error) {
	pipeline, err := LoadPipelineFromFile(context.ConfigFilename)
	if err != nil {
		return nil, nil, fmt.Errorf("Error parsing pipeline: %s", err)
	}
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
			return nil, nil, fmt.Errorf(
				"error reading job #%s metadata: %s",
				context.ArtifactsFromBuildNumber, err,
			)
		}

		codeVersion := otherMeta["jobsworth:code_version"]
		if codeVersion == "" {
			return nil, nil, fmt.Errorf(
				"build #%s does not have a recorded code version",
				context.ArtifactsFromBuildNumber,
			)
		}
		sourceCommitId := otherMeta["jobsworth:source_commit_id"]
		if sourceCommitId == "" {
			return nil, nil, fmt.Errorf(
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
		return nil, nil, fmt.Errorf("Error lowering pipeline: %s", err)
	}

	writeMetadata["jobsworth:code_version"] = context.CodeVersion
	writeMetadata["jobsworth:source_commit_id"] = context.SourceGitCommitId
	return bkSteps, writeMetadata, nil
}

func printSteps(bkSteps []interface{}, writeMetadata map[string]string) error {
	metadataYaml, err := yaml.Marshal(writeMetadata)
	if err != nil {
		return fmt.Errorf("Error marshalling metadata as yaml: %s", err)
	}
	fmt.Printf("# job metadata\n%s\n", string(metadataYaml))

	stepsYaml, err := MarshalPipelineSteps(bkSteps)
	if err != nil {
		return fmt.Errorf("Error marshalling steps as yaml: %s", err)
	}
	fmt.Printf("# pipeline\n%s", string(stepsYaml))
	return nil
}

func uploadSteps(context *Context, buildkite *Buildkite, bkSteps []interface{}, writeMetadata map[string]string) error {
	err := buildkite.WriteJobMetadata(writeMetadata)
	if err != nil {
		return fmt.Errorf("error writing job metadata: %s", err)
	}

	fmt.Fprintf(os.Stderr, "Usage: jobsworth <pipeline-file>\n\n")
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

	return commitObj.AsCommit()
}
