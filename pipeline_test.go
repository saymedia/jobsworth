package main

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v2"
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
	step = lowerStep(step, context, stepContext)
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
	step = lowerStep(step, context, stepContext)
	if !reflect.DeepEqual(step["env"], nil) {
		t.Errorf("env should not be set for a wait step")
	}
	if !reflect.DeepEqual(step["agents"], nil) {
		t.Errorf("agents should not be set for a wait step")
	}
}
