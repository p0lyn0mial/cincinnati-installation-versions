package main

import (
	"bytes"
	"io/ioutil"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
)

func versionOrDie(v string) *semver.Version {
	ver, err := semver.NewVersion(v)
	if err != nil {
		panic(err)
	}
	return ver
}

type RoundTripFunc func(req *http.Request) *http.Response

func (f RoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req), nil
}

func TestFetchGraph(t *testing.T) {
	tests := []struct {
		name          string
		inputFile     string
		channel       string
		arch          string
		statusCode    int
		expectedURL   string
		expectedGraph *CincinnatiGraph
		expectedError string
	}{
		{
			name:        "Valid response for amd64",
			inputFile:   "testdata/fetch-graph-scenario-1.json",
			channel:     "stable-4.16",
			arch:        "amd64",
			statusCode:  200,
			expectedURL: "https://api.openshift.com/api/upgrades_info/graph?channel=stable-4.16&arch=amd64",
			expectedGraph: &CincinnatiGraph{
				Nodes: []CincinnatiNode{
					{
						Version:  versionOrDie("4.16.1"),
						Payload:  "example-payload",
						Metadata: map[string]string{"io.openshift.upgrades.graph.release.channels": "stable-4.16, fast-4.16"},
					},
					{
						Version:  versionOrDie("4.16.2"),
						Payload:  "another-payload",
						Metadata: map[string]string{"io.openshift.upgrades.graph.release.channels": "stable-4.16"},
					},
				},
			},
			expectedError: "",
		},
		{
			name:          "Invalid JSON response",
			inputFile:     "testdata/fetch-graph-scenario-2.json",
			channel:       "stable-4.16",
			arch:          "amd64",
			statusCode:    200,
			expectedURL:   "https://api.openshift.com/api/upgrades_info/graph?channel=stable-4.16&arch=amd64",
			expectedGraph: nil,
			expectedError: "error parsing JSON",
		},
		{
			name:          "Non-200 status code response",
			inputFile:     "testdata/fetch-graph-scenario-1.json",
			channel:       "stable-4.16",
			arch:          "amd64",
			statusCode:    500,
			expectedURL:   "https://api.openshift.com/api/upgrades_info/graph?channel=stable-4.16&arch=amd64",
			expectedGraph: nil,
			expectedError: "error: status 500",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile(tc.inputFile)
			if err != nil {
				t.Fatalf("Failed to read test data file: %v", err)
			}

			client := &http.Client{
				Transport: RoundTripFunc(func(req *http.Request) *http.Response {
					if req.URL.String() != tc.expectedURL {
						t.Errorf("Unexpected URL: got %s, expected %s", req.URL.String(), tc.expectedURL)
					}
					return &http.Response{
						StatusCode: tc.statusCode,
						Body:       ioutil.NopCloser(bytes.NewReader(data)),
						Header:     make(http.Header),
					}
				}),
			}

			graph, err := fetchGraph(client, tc.channel, tc.arch)
			if tc.expectedError != "" {
				if err == nil {
					t.Fatalf("Expected error containing %q, but got none", tc.expectedError)
				}
				if !strings.Contains(err.Error(), tc.expectedError) {
					t.Errorf("Expected error containing %q, got %q", tc.expectedError, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("fetchGraph returned an error: %v", err)
			}
			if graph == nil {
				t.Fatal("Expected a non-nil graph, but got nil")
			}
			if !reflect.DeepEqual(graph, tc.expectedGraph) {
				t.Errorf("Expected graph %+v, got %+v", tc.expectedGraph, graph)
			}
		})
	}
}
