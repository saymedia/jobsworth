package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"time"

	buildkiteAgent "github.com/buildkite/agent/agent"
	buildkite "github.com/buildkite/agent/api"
	"github.com/buildkite/agent/retry"
	"gopkg.in/yaml.v2"
)

type Buildkite struct {
	// There are two different buildkite APIs in use here.
	// - The "agent" API is used to interact with the build and job that are
	//   currently in progress.
	// - The regular API is used to interact with pre-existing builds
	agentClient  *buildkite.Client
	apiURL       *url.URL
	apiToken     string
	jobId        string
	pipelineSlug string
}

func (c *Context) Buildkite() *Buildkite {
	clientBuilder := buildkiteAgent.APIClient{
		Endpoint: c.BuildkiteAgentEndpointURL,
		Token:    c.BuildkiteAgentAccessToken,
	}
	agentClient := clientBuilder.Create()

	apiURL, _ := url.Parse(fmt.Sprintf("https://api.buildkite.com/v2/organizations/%s/", c.BuildkiteOrganizationSlug))

	return &Buildkite{
		agentClient:  agentClient,
		apiToken:     c.BuildkiteAPIAccessToken,
		apiURL:       apiURL,
		jobId:        c.BuildkiteJobId,
		pipelineSlug: c.BuildkitePipelineSlug,
	}
}

func (b *Buildkite) WriteJobMetadata(metadata map[string]string) error {
	client := b.agentClient
	for k, v := range metadata {
		metadatum := &buildkite.MetaData{
			Key:   k,
			Value: v,
		}
		err := retry.Do(func(s *retry.Stats) error {
			resp, err := client.MetaData.Set(b.jobId, metadatum)
			if resp != nil && (resp.StatusCode == 401 || resp.StatusCode == 404) {
				s.Break()
			}

			return err
		}, &retry.Config{Maximum: 10, Interval: 1 * time.Second})

		if err != nil {
			return fmt.Errorf("error setting metadata %s: %s", k, err)
		}
	}
	return nil
}

func (b *Buildkite) InsertPipelineSteps(steps []interface{}) error {
	client := b.agentClient

	pipelineBytes, err := yaml.Marshal(map[string]interface{}{
		"steps": steps,
	})

	pipeline := &buildkite.Pipeline{
		UUID:     buildkite.NewUUID(),
		Data:     pipelineBytes,
		FileName: "pipeline.yaml",
	}
	_, err = client.Pipelines.Upload(b.jobId, pipeline)
	return err
}

func (b *Buildkite) ReadOtherBuildMetadata(number string) (map[string]string, error) {
	rawData, err := b.apiGET([]string{"pipelines", b.pipelineSlug, "builds", number})
	if err != nil {
		return nil, err
	}
	ret := map[string]string{}
	for k, v := range rawData["meta_data"].(map[string]interface{}) {
		ret[k] = v.(string)
	}
	return ret, nil
}

func (b *Buildkite) apiGET(pathParts []string) (map[string]interface{}, error) {
	urlPath := &url.URL{
		Path: strings.Join(pathParts, "/"),
	}
	reqURL := b.apiURL.ResolveReference(urlPath)

	req := &http.Request{
		Method: "GET",
		Header: http.Header{},
		URL:    reqURL,
	}
	req.Header.Add("Authorization", "Bearer "+b.apiToken)

	client := &http.Client{}

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	resBodyBytes, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("%s", res.Status)
	}

	ret := map[string]interface{}{}
	err = json.Unmarshal(resBodyBytes, &ret)
	return ret, err
}
