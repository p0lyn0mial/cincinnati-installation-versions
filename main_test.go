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

func TestDiscoverReleases(t *testing.T) {
	type fileResponse struct {
		filename   string
		statusCode int
	}

	tests := []struct {
		name         string
		startChannel string
		arch         string
		// responses maps URL -> fileResponse
		responses     map[string]fileResponse
		minVer        string
		minChannelVer string
		prefixes      []string
		expected      ReleasesByChannel
	}{
		{
			name:         "discovered additional channel (stable & fast)",
			startChannel: "stable-4.16",
			arch:         "amd64",
			responses: map[string]fileResponse{
				"https://api.openshift.com/api/upgrades_info/graph?channel=stable-4.16&arch=amd64": {filename: "testdata/discover-releases-scenario-1-stable-4.16.json", statusCode: 200},
				"https://api.openshift.com/api/upgrades_info/graph?channel=fast-4.16&arch=amd64":   {filename: "testdata/discover-releases-scenario-1-fast-4.16.json", statusCode: 200},
			},
			minVer:        "4.16.0",
			minChannelVer: "4.16",
			prefixes:      []string{"stable-", "fast-"},
			expected: ReleasesByChannel{
				"stable-4.16": {
					"4.16.2": []Release{
						{
							Version:  versionOrDie("4.16.2"),
							Channel:  "stable-4.16",
							Arch:     "amd64",
							Payload:  "payload-stable",
							Metadata: map[string]string{"io.openshift.upgrades.graph.release.channels": "stable-4.16, fast-4.16"},
						},
					},
				},
				"fast-4.16": {
					"4.16.3": []Release{
						{
							Version:  versionOrDie("4.16.3"),
							Channel:  "fast-4.16",
							Arch:     "amd64",
							Payload:  "payload-fast",
							Metadata: map[string]string{},
						},
					},
				},
			},
		},
		{
			name:         "discovered channel fails",
			startChannel: "stable-4.16",
			arch:         "amd64",
			responses: map[string]fileResponse{
				"https://api.openshift.com/api/upgrades_info/graph?channel=stable-4.16&arch=amd64": {filename: "testdata/discover-releases-scenario-2-stable-4.16.json", statusCode: 200},
				"https://api.openshift.com/api/upgrades_info/graph?channel=fast-4.16&arch=amd64":   {filename: "testdata/discover-releases-scenario-2-fast-4.16.json", statusCode: 500},
			},
			minVer:        "4.16.0",
			minChannelVer: "4.16",
			prefixes:      []string{"stable-", "fast-"},
			expected: ReleasesByChannel{
				"stable-4.16": {
					"4.16.2": []Release{
						{
							Version:  versionOrDie("4.16.2"),
							Channel:  "stable-4.16",
							Arch:     "amd64",
							Payload:  "payload-stable",
							Metadata: map[string]string{"io.openshift.upgrades.graph.release.channels": "stable-4.16, fast-4.16"},
						},
					},
				},
			},
			// TODO: add checking expected err
		},
		{
			name:         "no node meets minVer requirement",
			startChannel: "stable-4.16",
			arch:         "amd64",
			responses: map[string]fileResponse{
				"https://api.openshift.com/api/upgrades_info/graph?channel=stable-4.16&arch=amd64": {filename: "testdata/discover-releases-scenario-3-stable-4.16.json", statusCode: 200},
			},
			minVer:        "4.16.1",
			minChannelVer: "4.16",
			prefixes:      []string{"stable-", "fast-"},
			expected: ReleasesByChannel{
				"stable-4.16": map[string][]Release{},
			},
		},
		{
			name:         "discover releases from 4.16.1 to 4.18 via channels 4.17 and 4.18",
			startChannel: "stable-4.16",
			arch:         "amd64",
			responses: map[string]fileResponse{
				"https://api.openshift.com/api/upgrades_info/graph?channel=stable-4.16&arch=amd64": {filename: "testdata/discover-releases-scenario-4-stable-4.16.json", statusCode: 200},
				"https://api.openshift.com/api/upgrades_info/graph?channel=stable-4.17&arch=amd64": {filename: "testdata/discover-releases-scenario-4-stable-4.17.json", statusCode: 200},
				"https://api.openshift.com/api/upgrades_info/graph?channel=stable-4.18&arch=amd64": {filename: "testdata/discover-releases-scenario-4-stable-4.18.json", statusCode: 200},
			},
			minVer:        "4.16.1",
			minChannelVer: "4.16",
			prefixes:      []string{"stable-"},
			expected: ReleasesByChannel{
				"stable-4.16": {
					"4.16.2": []Release{
						{
							Version:  versionOrDie("4.16.2"),
							Channel:  "stable-4.16",
							Arch:     "amd64",
							Payload:  "payload-4.16",
							Metadata: map[string]string{"io.openshift.upgrades.graph.release.channels": "stable-4.17, stable-4.18"},
						},
					},
				},
				"stable-4.17": {
					"4.17.5": []Release{
						{
							Version:  versionOrDie("4.17.5"),
							Channel:  "stable-4.17",
							Arch:     "amd64",
							Payload:  "payload-4.17",
							Metadata: map[string]string{},
						},
					},
				},
				"stable-4.18": {
					"4.18.1": []Release{
						{
							Version:  versionOrDie("4.18.1"),
							Channel:  "stable-4.18",
							Arch:     "amd64",
							Payload:  "payload-4.18",
							Metadata: map[string]string{},
						},
					},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{
				Transport: RoundTripFunc(func(req *http.Request) *http.Response {
					url := req.URL.String()
					respMapping, ok := tc.responses[url]
					if !ok {
						t.Fatalf("No response mapping for URL: %s", url)
					}

					data, err := os.ReadFile(respMapping.filename)
					if err != nil {
						t.Fatalf("Failed to read file %s: %v", respMapping.filename, err)
					}

					return &http.Response{
						StatusCode: respMapping.statusCode,
						Body:       ioutil.NopCloser(bytes.NewReader(data)),
					}
				}),
			}

			minVer, err := semver.NewVersion(tc.minVer)
			if err != nil {
				t.Fatalf("Failed to parse minVer: %v", err)
			}
			minChannelVer, err := dropPatch(minVer)
			if err != nil {
				t.Fatalf("Failed to drop patch from minVer: %v", err)
			}

			releases := discoverReleases(client, tc.startChannel, minVer, minChannelVer, tc.prefixes, tc.arch)
			if !reflect.DeepEqual(releases, tc.expected) {
				t.Errorf("Expected releases %+v, got %+v", tc.expected, releases)
			}
		})
	}
}
