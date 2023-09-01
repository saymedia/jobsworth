package main

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"io/ioutil"
	"strings"

	"github.com/hashicorp/hil"
	hilAST "github.com/hashicorp/hil/ast"
	"gopkg.in/yaml.v2"
)

func init() {
	gob.Register(make(map[interface{}]interface{}))
	gob.Register(make([]interface{}, 0))
}

type Pipeline struct {
	SmokeTest          []Step   `yaml:"smoke_test"`
	Build              []Step   `yaml:"build"`
	Deploy             []Step   `yaml:"deploy"`
	ValidationTest     []Step   `yaml:"validation_test"`
	TrivialDeployEnvs  []string `yaml:"trivial_deploy_environments"`
	CautiousDeployEnvs []string `yaml:"cautious_deploy_environments"`
}

type Step map[string]interface{}

func LoadPipelineFromFile(fn string) (*Pipeline, error) {
	configBytes, err := ioutil.ReadFile(fn)
	if err != nil {
		return nil, err
	}

	pipeline := &Pipeline{}
	err = yaml.Unmarshal(configBytes, pipeline)
	if err != nil {
		return nil, fmt.Errorf("parse error: %s", err)
	}

	return pipeline, nil
}

func MarshalPipelineSteps(steps []interface{}) ([]byte, error) {
	return yaml.Marshal(map[string]interface{}{
		"steps": steps,
	})
}

func (p *Pipeline) Lower(context *Context) ([]interface{}, error) {
	// Now we lower the configuration into Buildkite's level of abstraction,
	// which is just a flat list of steps with sync/block points.
	// This is not a []Step because Buildkite models sync points as
	// a string containing literally "wait".
	bkSteps := make([]interface{}, 0, 20)

	if context.ArtifactsFromBuildNumber == "" {
		if len(p.SmokeTest) > 0 {
			stepContext := &StepContext{
				EnvironmentName: context.BuildEnvironment,
				QueueName:       "smoke_test",
				EmojiName:       "interrobang",
			}
			loweredSteps, err := lowerSteps(
				p.SmokeTest, context, stepContext,
			)
			if err != nil {
				return nil, err
			}
			bkSteps = append(bkSteps, bkWait)
			bkSteps = append(bkSteps, loweredSteps...)
		}
	}

	if context.BranchName == "master" {

		if context.ArtifactsFromBuildNumber == "" {
			if len(p.Build) > 0 {
				stepContext := &StepContext{
					EnvironmentName: context.BuildEnvironment,
					QueueName:       "build",
					EmojiName:       "package",
				}
				loweredSteps, err := lowerSteps(
					p.Build, context, stepContext,
				)
				if err != nil {
					return nil, err
				}
				bkSteps = append(bkSteps, bkWait)
				bkSteps = append(bkSteps, loweredSteps...)
			}
		}

		if len(p.Deploy) > 0 {

			trivialEnvs := p.TrivialDeployEnvs
			cautiousEnvs := p.CautiousDeployEnvs

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
						EnvironmentName:    envName,
						QueueName:          "deploy",
						EmojiName:          "truck",
						PreventConcurrency: true,
					}
					loweredSteps, err := lowerSteps(
						p.Deploy, context, stepContext,
					)
					if err != nil {
						return nil, err
					}
					bkSteps = append(bkSteps, loweredSteps...)
				}

				if len(p.ValidationTest) > 0 {
					bkSteps = append(bkSteps, bkWait)
					for _, envName := range trivialEnvs {
						stepContext := &StepContext{
							EnvironmentName:    envName,
							QueueName:          "validation_test",
							EmojiName:          "curly_loop",
							PreventConcurrency: true,
						}
						loweredSteps, err := lowerSteps(
							p.ValidationTest, context, stepContext,
						)
						if err != nil {
							return nil, err
						}
						bkSteps = append(bkSteps, loweredSteps...)
					}
				}
			}

			for _, envName := range cautiousEnvs {
				deployContext := &StepContext{
					EnvironmentName:    envName,
					QueueName:          "deploy",
					EmojiName:          "truck",
					Cautious:           true,
					PreventConcurrency: true,
				}
				validateContext := &StepContext{
					EnvironmentName:    envName,
					QueueName:          "validation_test",
					EmojiName:          "curly_loop",
					PreventConcurrency: true,
				}

				// Manual deploys run sequentially, so that they can
				// potentially add blocking steps.
				bkSteps = append(bkSteps, bkWait)

				loweredSteps, err := lowerSteps(
					p.Deploy, context, deployContext,
				)
				if err != nil {
					return nil, err
				}
				bkSteps = append(bkSteps, loweredSteps...)

				if len(p.ValidationTest) > 0 {
					loweredSteps, err := lowerSteps(
						p.ValidationTest, context, validateContext,
					)
					if err != nil {
						return nil, err
					}
					bkSteps = append(bkSteps, bkWait)
					bkSteps = append(bkSteps, loweredSteps...)
				}
			}
		}
	}

	return bkSteps, nil
}

func lowerStep(step Step, context *Context, stepContext *StepContext) (Step, error) {
	step = deepCopyStep(step)

	err := interpolateStep(step, context, stepContext)
	if err != nil {
		return nil, err
	}

	agents, ok := step["agents"].(map[interface{}]interface{})
	if !ok {
		agents = make(map[interface{}]interface{})
		if _, exists := step["wait"]; !exists {
			step["agents"] = agents
		}
	}
	agents["queue"] = stepContext.QueueName
	agents["environment"] = stepContext.EnvironmentName

	env, ok := step["env"].(map[interface{}]interface{})
	if !ok {
		env = make(map[interface{}]interface{})
		if _, exists := step["wait"]; !exists {
			step["env"] = env
		}
	}
	env["JOBSWORTH_CAUTIOUS"] = stepContext.CautiousStr()
	env["JOBSWORTH_CODEBASE"] = context.CodebaseName()
	env["JOBSWORTH_CODE_VERSION"] = context.CodeVersion
	env["JOBSWORTH_SOURCE_GIT_COMMIT_ID"] = context.SourceGitCommitId
	env["JOBSWORTH_ENVIRONMENT"] = stepContext.EnvironmentName

	name, ok := step["name"].(string)
	if !ok {
		name = ""
	}
	step["name"] = strings.TrimSpace(fmt.Sprintf(":%s: %s", stepContext.EmojiName, name))

	if step["command"] != nil && stepContext.PreventConcurrency &&
		step["concurrency"] == nil && step["concurrency_group"] == nil {
		step["concurrency_group"] = fmt.Sprintf("%s/%s", stepContext.EnvironmentName, context.BuildkitePipelineSlug)
		step["concurrency"] = 1
		if step["concurrency_method"] == nil {
			step["concurrency_method"] = "eager"
		}
	}

	return step, nil
}

func lowerSteps(steps []Step, context *Context, stepContext *StepContext) ([]interface{}, error) {
	ret := make([]interface{}, len(steps))
	for i, step := range steps {
		loweredStep, err := lowerStep(step, context, stepContext)
		if err != nil {
			return nil, fmt.Errorf("step %d, %s", i, err)
		}
		ret[i] = loweredStep
	}
	return ret, nil
}

// Modifies a step in-place to expand all of the interpolation expressions
func interpolateStep(step Step, context *Context, stepContext *StepContext) error {
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
			"source_git_commit": {
				Value: context.SourceGitCommitId,
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
	return hil.Walk(step, func(d *hil.WalkData) error {
		result, _, err := hil.Eval(d.Root, evalConfig)
		if err != nil {
			// Unfortunately, there is no way to know which field gave an error
			return fmt.Errorf("%s %s", d.Location, err)
		}
		d.Replace = true
		d.ReplaceValue = result.(string)
		return nil
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
