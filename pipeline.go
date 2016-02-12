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
			bkSteps = append(bkSteps, bkWait)
			bkSteps = append(bkSteps, lowerSteps(
				p.SmokeTest, context, stepContext,
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
			if len(p.Build) > 0 {
				stepContext := &StepContext{
					EnvironmentName: context.BuildEnvironment,
					QueueName:       "build",
					EmojiName:       "package",
				}
				bkSteps = append(bkSteps, bkWait)
				bkSteps = append(bkSteps, lowerSteps(
					p.Build, context, stepContext,
				)...)
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
						EnvironmentName: envName,
						QueueName:       "deploy",
						EmojiName:       "truck",
					}
					bkSteps = append(bkSteps, lowerSteps(
						p.Deploy, context, stepContext,
					)...)
				}

				if len(p.ValidationTest) > 0 {
					bkSteps = append(bkSteps, bkWait)
					for _, envName := range trivialEnvs {
						stepContext := &StepContext{
							EnvironmentName: envName,
							QueueName:       "validation_test",
							EmojiName:       "curly_loop",
						}
						bkSteps = append(bkSteps, lowerSteps(
							p.ValidationTest, context, stepContext,
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
					p.Deploy, context, deployContext,
				)...)

				if len(p.ValidationTest) > 0 {
					bkSteps = append(bkSteps, bkWait)
					bkSteps = append(bkSteps, lowerSteps(
						p.ValidationTest, context, validateContext,
					)...)
				}
			}
		}
	}

	return bkSteps, nil
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
