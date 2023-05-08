package main

import (
	"github.com/go-test/deep"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"reflect"
	"strings"
	"testing"
)

func TestDeepCopyStep(t *testing.T) {
	// pipeline := &Pipeline{}
	step := Step{}
	stepBytes := []byte(`
command: my command
agents:
  mytag: myval
plugins:
  - docker#v3.7.0:
      image: "node:12.19-alpine-3.12"
`)
	err := yaml.Unmarshal(stepBytes, &step)
	if err != nil {
		t.Error("unmarshal failed:", err)
	}
	if step["command"] != "my command" {
		t.Error("failed to parse step", step["command"])
	}
	deepCopyStep(step)
}

func TestWaitStep(t *testing.T) {
	context := &Context{}
	stepContext := &StepContext{}

	step := Step{}
	stepBytes := []byte(`
command: foo
`)
	yaml.Unmarshal(stepBytes, &step)
	step, err := lowerStep(step, context, stepContext)
	if err != nil {
		t.Error("lowerStep returned err:", err)
	}
	if reflect.DeepEqual(step["env"], nil) {
		t.Errorf("env should be set")
	}
	if reflect.DeepEqual(step["agents"], nil) {
		t.Errorf("agents should be set")
	}

	step = Step{}
	stepBytes = []byte(`
wait: ~
`)
	yaml.Unmarshal(stepBytes, &step)
	step, err = lowerStep(step, context, stepContext)
	if err != nil {
		t.Error("lowerStep returned err:", err)
	}
	if !reflect.DeepEqual(step["env"], nil) {
		t.Errorf("env should not be set for a wait step")
	}
	if !reflect.DeepEqual(step["agents"], nil) {
		t.Errorf("agents should not be set for a wait step")
	}
}

func TestInterpolate(t *testing.T) {
	context := &Context{}
	stepContext := &StepContext{
		EnvironmentName: "myenv",
	}

	step := Step{}
	stepBytes := []byte(`x: ${environment}`)
	if err := yaml.Unmarshal(stepBytes, &step); err != nil {
		t.Error("unmarshal error", err)
	}
	step, err := lowerStep(step, context, stepContext)
	if err != nil {
		t.Error("lowerStep returned err:", err)
	}
	actual := step["x"]
	expected := "myenv"
	if actual != expected {
		t.Error("interpolate value does not match", actual, expected)
	}
}

func TestInterpolateUnknownVariable(t *testing.T) {
	context := &Context{}
	stepContext := &StepContext{}
	step := Step{}
	stepBytes := []byte(`x: ${badvar}`)
	if err := yaml.Unmarshal(stepBytes, &step); err != nil {
		t.Error("unmarshal error", err)
	}
	step, err := lowerStep(step, context, stepContext)
	if err == nil {
		t.Error("lowerStep should error when a bad variable is referenced")
	}
	if !strings.Contains(err.Error(), "unknown variable accessed: badvar") {
		t.Error("message should say something about bad varaible", err.Error())
	}
}

func testGenerateSteps(t *testing.T, isMaster bool, sourcePath, expectedPath string) {
	context := Context{
		ConfigFilename:        sourcePath,
		BuildkitePipelineSlug: "myrepo",
	}
	if isMaster {
		context.BranchName = "master"
	}
	buildkite := DryRunBuildMetadataClient{}
	bkSteps, _, err := generateSteps(&context, &buildkite)
	if err != nil {
		t.Error("generateSteps returned err:", err)
	}
	bkStepsMarshalled, err := MarshalPipelineSteps(bkSteps)
	if err != nil {
		t.Error("marshal returned err:", err)
	}
	expectedBytes, err := ioutil.ReadFile(expectedPath)
	if err != nil {
		t.Error("reading expected file error", err)
	}
	var expected, actual interface{}
	err = yaml.Unmarshal(bkStepsMarshalled, &actual)
	if err != nil {
		t.Error("parsing expected marshalled error", err)
	}
	err = yaml.Unmarshal(expectedBytes, &expected)
	if err != nil {
		t.Error("parsing expected file error", err)
	}
	if diff := deep.Equal(expected, actual); diff != nil {
		t.Error(diff, string(bkStepsMarshalled))
	}
}

func TestGenerateSnapshots(t *testing.T) {
	testGenerateSteps(t, false, "testdata/basic.in.yaml", "testdata/basic_non_master.out.yaml")
	testGenerateSteps(t, true, "testdata/basic.in.yaml", "testdata/basic_master.out.yaml")
}
