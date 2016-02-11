package main

import (
	"bytes"
	"encoding/gob"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/hil"
	hilAST "github.com/hashicorp/hil/ast"
	"gopkg.in/libgit2/git2go.v22"
	"gopkg.in/yaml.v2"
)

var rollbackMessageRegexp = regexp.MustCompile("^[Rr]oll\\s*back\\s+(to\\s+)?#?(\\d+)")
var envOverrideMessageRegexp = regexp.MustCompile("^[Dd]eploy\\s*(#?(\\d+)\\s*)?(to\\s+)?(\\S+)")

type Step map[string]interface{}

type Pipeline struct {
	SmokeTest          []Step   `yaml:"smoke_test"`
	Build              []Step   `yaml:"build"`
	Deploy             []Step   `yaml:"deploy"`
	ValidationTest     []Step   `yaml:"validation_test"`
	TrivialDeployEnvs  []string `yaml:"trivial_deploy_environments"`
	CautiousDeployEnvs []string `yaml:"cautious_deploy_environments"`
}

type Context struct {
	BuildNumber                   uint64
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

const bkWait = "wait"

func init() {
	gob.Register(make(map[interface{}]interface{}))
}

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
		ConfigFilename:   args[0],
		BranchName:       os.Getenv("BUILDKITE_BRANCH"),
		BuildMessage:     os.Getenv("BUILDKITE_MESSAGE"),
		RepoURL:          os.Getenv("BUILDKITE_REPO"),
		BuildEnvironment: os.Getenv("JOBSWORTH_ENVIRONMENT"),
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
	configBytes, err := ioutil.ReadFile(context.ConfigFilename)
	if err != nil {
		return err
	}

	pipeline := &Pipeline{}
	err = yaml.Unmarshal(configBytes, pipeline)
	if err != nil {
		return fmt.Errorf("Error parsing pipeline: %s", err)
	}

	// Now we lower the configuration into Buildkite's level of abstraction,
	// which is just a flat list of steps with sync/block points.
	// This is not a []Step because Buildkite models sync points as
	// a string containing literally "wait".
	bkSteps := make([]interface{}, 0, 20)

	if context.ArtifactsFromBuildNumber == "" {
		if len(pipeline.SmokeTest) > 0 {
			stepContext := &StepContext{
				EnvironmentName: context.BuildEnvironment,
				QueueName:       "smoke_test",
				EmojiName:       "interrobang",
			}
			bkSteps = append(bkSteps, bkWait)
			bkSteps = append(bkSteps, lowerSteps(
				pipeline.SmokeTest, context, stepContext,
			)...)
		}
	}

	if context.BranchName == "master" {

		if context.ArtifactsFromBuildNumber != "" {
			// Create a "synthetic" build step that uses the
			// jobsworth-copy-artifact-meta helper program to
			// copy the metadata keys from the given build
			// to try to re-use its artifacts for this deployment.
			synthStep := Step{
				"command": fmt.Sprintf(
					"jobsworth-copy-artifact-meta \"%s\"",
					context.ArtifactsFromBuildNumber,
				),
				"label": fmt.Sprintf(
					"Artifacts from #%s",
					context.ArtifactsFromBuildNumber,
				),
			}
			stepContext := &StepContext{
				EnvironmentName: context.BuildEnvironment,
				QueueName:       "plan_pipeline",
				EmojiName:       "repeat",
			}
			bkSteps = append(bkSteps, bkWait)
			bkSteps = append(bkSteps, lowerStep(
				synthStep, context, stepContext,
			))
		} else {
			if len(pipeline.Build) > 0 {
				stepContext := &StepContext{
					EnvironmentName: context.BuildEnvironment,
					QueueName:       "build",
					EmojiName:       "package",
				}
				bkSteps = append(bkSteps, bkWait)
				bkSteps = append(bkSteps, lowerSteps(
					pipeline.Build, context, stepContext,
				)...)
			}
		}

		if len(pipeline.Deploy) > 0 {

			trivialEnvs := pipeline.TrivialDeployEnvs
			cautiousEnvs := pipeline.CautiousDeployEnvs

			if context.OverrideDeployEnvironmentName != "" {
				trivialEnvs = []string{}
				cautiousEnvs = []string{context.OverrideDeployEnvironmentName}
			}

			if len(trivialEnvs) > 0 {
				bkSteps = append(bkSteps, bkWait)

				// Trivial deploys to different environments can run
				// concurrently
				for _, envName := range trivialEnvs {
					stepContext := &StepContext{
						EnvironmentName: envName,
						QueueName:       "deploy",
						EmojiName:       "truck",
					}
					bkSteps = append(bkSteps, lowerSteps(
						pipeline.Deploy, context, stepContext,
					)...)
				}

				if len(pipeline.ValidationTest) > 0 {
					bkSteps = append(bkSteps, bkWait)
					for _, envName := range trivialEnvs {
						stepContext := &StepContext{
							EnvironmentName: envName,
							QueueName:       "validation_test",
							EmojiName:       "curly_loop",
						}
						bkSteps = append(bkSteps, lowerSteps(
							pipeline.ValidationTest, context, stepContext,
						)...)
					}
				}
			}

			for _, envName := range cautiousEnvs {
				deployContext := &StepContext{
					EnvironmentName: envName,
					QueueName:       "deploy",
					EmojiName:       "truck",
					Cautious:        true,
				}
				validateContext := &StepContext{
					EnvironmentName: envName,
					QueueName:       "validation_test",
					EmojiName:       "curly_loop",
				}

				// Manual deploys run sequentially, so that they can
				// potentially add blocking steps.
				bkSteps = append(bkSteps, bkWait)

				bkSteps = append(bkSteps, lowerSteps(
					pipeline.Deploy, context, deployContext,
				)...)

				if len(pipeline.ValidationTest) > 0 {
					bkSteps = append(bkSteps, bkWait)
					bkSteps = append(bkSteps, lowerSteps(
						pipeline.ValidationTest, context, validateContext,
					)...)
				}
			}
		}

	}

	outputBytes, err := yaml.Marshal(map[string]interface{}{
		"steps": bkSteps,
	})
	os.Stdout.Write(outputBytes)

	return nil
}

func lowerStep(step Step, context *Context, stepContext *StepContext) Step {
	step = deepCopyStep(step)

	interpolateStep(step, context, stepContext)

	agents, ok := step["agents"].(map[interface{}]interface{})
	if !ok {
		agents = make(map[interface{}]interface{})
		step["agents"] = agents
	}
	agents["queue"] = stepContext.QueueName
	agents["environment"] = stepContext.EnvironmentName

	env, ok := step["env"].(map[interface{}]interface{})
	if !ok {
		env = make(map[interface{}]interface{})
		step["env"] = env
	}
	env["JOBSWORTH_CAUTIOUS"] = stepContext.CautiousStr()
	env["JOBSWORTH_CODEBASE"] = context.CodebaseName()
	env["JOBSWORTH_CODE_VERSION"] = context.CodeVersion
	env["JOBSWORTH_ENVIRONMENT"] = stepContext.EnvironmentName

	label, ok := step["label"].(string)
	if !ok {
		label = ""
	}
	step["label"] = strings.TrimSpace(fmt.Sprintf(":%s: %s", stepContext.EmojiName, label))

	return step
}

func lowerSteps(steps []Step, context *Context, stepContext *StepContext) []interface{} {
	ret := make([]interface{}, len(steps))
	for i, step := range steps {
		ret[i] = lowerStep(step, context, stepContext)
	}
	return ret
}

// Modifies a step in-place to expand all of the interpolation expressions
func interpolateStep(step Step, context *Context, stepContext *StepContext) {
	scope := &hilAST.BasicScope{
		VarMap: map[string]hilAST.Variable{
			"environment": {
				Value: stepContext.EnvironmentName,
				Type:  hilAST.TypeString,
			},
			"branch": {
				Value: context.BranchName,
				Type:  hilAST.TypeString,
			},
			"codebase": {
				Value: context.CodebaseName(),
				Type:  hilAST.TypeString,
			},
			"code_version": {
				Value: context.CodeVersion,
				Type:  hilAST.TypeString,
			},
			"cautious": {
				Value: stepContext.CautiousStr(),
				Type:  hilAST.TypeString,
			},
		},
	}
	evalConfig := &hil.EvalConfig{
		GlobalScope: scope,
	}
	hil.Walk(step, func(d *hil.WalkData) error {
		result, _, err := hil.Eval(d.Root, evalConfig)
		if err == nil {
			d.Replace = true
			d.ReplaceValue = result.(string)
		}
		return err
	})
}

func deepCopyStep(in Step) Step {
	// We use gob as a lazy way to get a deep copy of the step
	// before we modify it.
	buf := &bytes.Buffer{}
	enc := gob.NewEncoder(buf)
	dec := gob.NewDecoder(buf)

	var out Step

	if err := enc.Encode(&in); err != nil {
		panic(err)
	}
	if err := dec.Decode(&out); err != nil {
		panic(err)
	}

	return out
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
