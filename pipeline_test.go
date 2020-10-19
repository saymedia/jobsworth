package main

import (
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
