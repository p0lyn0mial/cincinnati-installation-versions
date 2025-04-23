package cincinnaticlient

import (
	"bytes"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/hashicorp/go-version"
)

func versionOrDie(v string) *version.Version {
	ver, err := version.NewVersion(v)
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
		graphURL      *url.URL
		inputFile     string
		channel       string
		arch          string
		statusCode    int
		expectedURL   string
		expectedGraph *Graph
		expectedError string
	}{
		{
			name:          "no graph url provided",
			inputFile:     "testdata/fetch-graph-valid-response.json",
			channel:       "stable-4.16",
			arch:          "amd64",
			expectedError: "cincinnati graph URL is required",
		},
		{
			name:        "valid response for amd64",
			graphURL:    rawURLtoURLOrDie("https://api.openshift.com/api/upgrades_info/graph"),
			inputFile:   "testdata/fetch-graph-valid-response.json",
			channel:     "stable-4.16",
			arch:        "amd64",
			statusCode:  200,
			expectedURL: "https://api.openshift.com/api/upgrades_info/graph?arch=amd64&channel=stable-4.16",
			expectedGraph: &Graph{
				Nodes: []Node{
					{
						Version:  versionOrDie("4.16.1"),
						Payload:  "example-payload",
						Metadata: map[string]string{"io.openshift.upgrades.graph.release.channels": "stable-4.16,fast-4.16"},
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
			name:          "invalid JSON response",
			graphURL:      rawURLtoURLOrDie("https://api.openshift.com/api/upgrades_info/graph"),
			inputFile:     "testdata/fetch-graph-invalid-response.json",
			channel:       "stable-4.16",
			arch:          "amd64",
			statusCode:    200,
			expectedURL:   "https://api.openshift.com/api/upgrades_info/graph?arch=amd64&channel=stable-4.16",
			expectedGraph: nil,
			expectedError: "error parsing JSON",
		},
		{
			name:          "non-200 status code response",
			graphURL:      rawURLtoURLOrDie("https://api.openshift.com/api/upgrades_info/graph"),
			inputFile:     "testdata/fetch-graph-valid-response.json",
			channel:       "stable-4.16",
			arch:          "amd64",
			statusCode:    500,
			expectedURL:   "https://api.openshift.com/api/upgrades_info/graph?arch=amd64&channel=stable-4.16",
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

			hClient := &http.Client{
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

			target := New(hClient)

			graph, err := target.fetchGraph(tc.graphURL, tc.channel, tc.arch)
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
		graphURL     *url.URL
		startChannel string
		arch         string
		// responses maps URL -> fileResponse
		responses                   map[string]fileResponse
		allowedConditionalEdgeRisks []string
		expected                    ReleasesByChannel
		expectedError               string
	}{
		{
			name:         "discovers only a single channel (even if multiple are available)",
			graphURL:     rawURLtoURLOrDie("https://api.openshift.com/api/upgrades_info/graph"),
			startChannel: "stable-4.16",
			arch:         "amd64",
			responses: map[string]fileResponse{
				"https://api.openshift.com/api/upgrades_info/graph?arch=amd64&channel=stable-4.16": {filename: "testdata/discover-releases-stable-4.16.json", statusCode: 200},
			},
			expected: ReleasesByChannel{
				"stable-4.16": {
					"4.16.2": Release{
						Version: "4.16.2",
						Arch:    "amd64",
						Payload: "payload-stable",
					},
				},
			},
		},
		{
			name:         "fails to discover a channel",
			graphURL:     rawURLtoURLOrDie("https://api.openshift.com/api/upgrades_info/graph"),
			startChannel: "fast-4.16",
			arch:         "amd64",
			responses: map[string]fileResponse{
				"https://api.openshift.com/api/upgrades_info/graph?arch=amd64&channel=fast-4.16": {filename: "testdata/discover-releases-fast-4.16.json", statusCode: 500},
			},
			expected: ReleasesByChannel{
				"stable-4.16": {
					"4.16.2": Release{
						Version: "4.16.2",
						Arch:    "amd64",
						Payload: "payload-stable",
					},
				},
			},
			expectedError: "error fetching amd64 graph for channel fast-4.16: error: status 500 when fetching data from",
		},
		{
			name:         "discover releases from 4.16.1 to 4.18 via channels 4.17 and 4.18",
			graphURL:     rawURLtoURLOrDie("https://api.openshift.com/api/upgrades_info/graph"),
			startChannel: "stable-4.16",
			arch:         "amd64",
			responses: map[string]fileResponse{
				"https://api.openshift.com/api/upgrades_info/graph?arch=amd64&channel=stable-4.16": {filename: "testdata/discover-releases-stable-4.16-with-4.17-4.18.json", statusCode: 200},
				"https://api.openshift.com/api/upgrades_info/graph?arch=amd64&channel=stable-4.17": {filename: "testdata/discover-releases-stable-4.17.json", statusCode: 200},
				"https://api.openshift.com/api/upgrades_info/graph?arch=amd64&channel=stable-4.18": {filename: "testdata/discover-releases-stable-4.18.json", statusCode: 200},
			},
			expected: ReleasesByChannel{
				"stable-4.16": {
					"4.16.2": Release{
						Version: "4.16.2",
						Arch:    "amd64",
						Payload: "payload-stable",
					},
				},
				"stable-4.17": {
					"4.17.5": Release{
						Version: "4.17.5",
						Arch:    "amd64",
						Payload: "payload-4.17",
					},
				},
				"stable-4.18": {
					"4.18.1": Release{
						Version: "4.18.1",
						Arch:    "amd64",
						Payload: "payload-4.18",
					},
				},
			},
		},
		{
			name:         "single channel with edges",
			graphURL:     rawURLtoURLOrDie("https://api.openshift.com/api/upgrades_info/graph"),
			startChannel: "stable-4.16",
			arch:         "amd64",
			responses: map[string]fileResponse{
				"https://api.openshift.com/api/upgrades_info/graph?arch=amd64&channel=stable-4.16": {filename: "testdata/discover-releases-stable-4.16-edges.json", statusCode: 200},
			},
			expected: ReleasesByChannel{
				"stable-4.16": VersionReleases{
					"4.16.1": Release{
						Version:           "4.16.1",
						Arch:              "amd64",
						Payload:           "payload-4.16.1",
						AvailableUpgrades: []string{"4.16.2", "4.16.5"},
					},
					"4.16.2": Release{
						Version:           "4.16.2",
						Arch:              "amd64",
						Payload:           "payload-4.16.2",
						AvailableUpgrades: []string{"4.16.5"},
					},
					"4.16.5": Release{
						Version: "4.16.5",
						Arch:    "amd64",
						Payload: "payload-4.16.5",
					},
				},
			},
		},
		{
			name:                        "conditional edges, all allowed",
			graphURL:                    rawURLtoURLOrDie("https://api.openshift.com/api/upgrades_info/graph"),
			startChannel:                "stable-4.16",
			arch:                        "amd64",
			allowedConditionalEdgeRisks: []string{"RiskA", "RiskB"},
			responses: map[string]fileResponse{
				"https://api.openshift.com/api/upgrades_info/graph?arch=amd64&channel=stable-4.16": {filename: "testdata/discover-releases-stable-4.16-conditional-edges.json", statusCode: 200},
			},
			expected: ReleasesByChannel{
				"stable-4.16": VersionReleases{
					"4.16.1": Release{
						Version:           "4.16.1",
						Arch:              "amd64",
						Payload:           "payload-4.16.1",
						AvailableUpgrades: []string{"4.16.3"},
					},
					"4.16.3": Release{
						Version: "4.16.3",
						Arch:    "amd64",
						Payload: "payload-4.16.3",
					},
				},
			},
		},
		{
			name:                        "conditional edges, partial allowed",
			graphURL:                    rawURLtoURLOrDie("https://api.openshift.com/api/upgrades_info/graph"),
			startChannel:                "stable-4.16",
			arch:                        "amd64",
			allowedConditionalEdgeRisks: []string{"RiskA"},
			responses: map[string]fileResponse{
				"https://api.openshift.com/api/upgrades_info/graph?arch=amd64&channel=stable-4.16": {filename: "testdata/discover-releases-stable-4.16-conditional-edges.json", statusCode: 200},
			},
			expected: ReleasesByChannel{
				"stable-4.16": VersionReleases{
					"4.16.1": Release{
						Version: "4.16.1",
						Arch:    "amd64",
						Payload: "payload-4.16.1",
					},
					"4.16.3": Release{
						Version: "4.16.3",
						Arch:    "amd64",
						Payload: "payload-4.16.3",
					},
				},
			},
		},
		{
			name:                        "conditional edges, none allowed",
			graphURL:                    rawURLtoURLOrDie("https://api.openshift.com/api/upgrades_info/graph"),
			startChannel:                "stable-4.16",
			arch:                        "amd64",
			allowedConditionalEdgeRisks: []string{},
			responses: map[string]fileResponse{
				"https://api.openshift.com/api/upgrades_info/graph?arch=amd64&channel=stable-4.16": {filename: "testdata/discover-releases-stable-4.16-conditional-edges.json", statusCode: 200},
			},
			expected: ReleasesByChannel{
				"stable-4.16": VersionReleases{
					"4.16.1": Release{
						Version: "4.16.1",
						Arch:    "amd64",
						Payload: "payload-4.16.1",
					},
					"4.16.3": Release{
						Version: "4.16.3",
						Arch:    "amd64",
						Payload: "payload-4.16.3",
					},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hClient := &http.Client{
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

			target := New(hClient)

			releases, err := target.DiscoverReleases(tc.graphURL, tc.startChannel, tc.arch, tc.allowedConditionalEdgeRisks)

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
				t.Fatalf("Failed to discover releases: %v", err)
			}
			if diff := cmp.Diff(tc.expected, releases); diff != "" {
				t.Errorf("Releases mismatch (-expected +got):\n%s", diff)
			}
		})
	}
}

func TestAggregateReleasesByChannelGroup(t *testing.T) {
	type testCase struct {
		name     string
		input    ReleasesByChannel
		expected ReleasesByChannel
	}

	testCases := []testCase{
		{
			name: "single channel, no merging",
			input: ReleasesByChannel{
				"stable-4.16": VersionReleases{
					"4.16.1": Release{
						Version:           "4.16.1",
						Arch:              "amd64",
						Payload:           "p1",
						AvailableUpgrades: []string{"4.16.2"},
					},
				},
			},
			expected: ReleasesByChannel{
				"stable": VersionReleases{
					"4.16.1": Release{
						Version:           "4.16.1",
						Arch:              "amd64",
						Payload:           "p1",
						AvailableUpgrades: []string{"4.16.2"},
					},
				},
			},
		},
		{
			name: "two channels with same prefix, merging AvailableUpgrades",
			input: ReleasesByChannel{
				"stable-4.16": VersionReleases{
					"4.16.1": Release{
						Version:           "4.16.1",
						Arch:              "amd64",
						Payload:           "p1",
						AvailableUpgrades: []string{"4.16.2"},
					},
					"4.16.2": Release{
						Version:           "4.16.2",
						Arch:              "amd64",
						Payload:           "p2",
						AvailableUpgrades: []string{},
					},
				},
				"stable-4.17": VersionReleases{
					"4.16.1": Release{
						Version:           "4.16.1",
						Arch:              "amd64",
						Payload:           "p1",
						AvailableUpgrades: []string{"4.16.5"},
					},
					"4.16.3": Release{
						Version:           "4.16.3",
						Arch:              "amd64",
						Payload:           "p3",
						AvailableUpgrades: []string{"4.16.7"},
					},
				},
			},
			expected: ReleasesByChannel{
				"stable": VersionReleases{
					"4.16.1": Release{
						Version:           "4.16.1",
						Arch:              "amd64",
						Payload:           "p1",
						AvailableUpgrades: []string{"4.16.2", "4.16.5"},
					},
					"4.16.2": Release{
						Version:           "4.16.2",
						Arch:              "amd64",
						Payload:           "p2",
						AvailableUpgrades: []string{},
					},
					"4.16.3": Release{
						Version:           "4.16.3",
						Arch:              "amd64",
						Payload:           "p3",
						AvailableUpgrades: []string{"4.16.7"},
					},
				},
			},
		},
		{
			name: "merge AvailableUpgrades without duplications",
			input: ReleasesByChannel{
				"stable-4.16": VersionReleases{
					"4.16.1": Release{
						Version:           "4.16.1",
						Arch:              "amd64",
						Payload:           "p1",
						AvailableUpgrades: []string{"4.16.2"},
					},
				},
				"stable-4.17": VersionReleases{
					"4.16.1": Release{
						Version:           "4.16.1",
						Arch:              "amd64",
						Payload:           "p1",
						AvailableUpgrades: []string{"4.16.2", "4.16.5"},
					},
				},
			},
			expected: ReleasesByChannel{
				"stable": VersionReleases{
					"4.16.1": Release{
						Version:           "4.16.1",
						Arch:              "amd64",
						Payload:           "p1",
						AvailableUpgrades: []string{"4.16.2", "4.16.5"},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := AggregateReleasesByChannelGroupAndSortAvailableUpgrades(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if diff := cmp.Diff(result, tc.expected); diff != "" {
				t.Errorf("Unexpected output (-expected +got):\n%s", diff)
			}
		})
	}
}

func rawURLtoURLOrDie(rawURL string) *url.URL {
	u, err := url.Parse(rawURL)
	if err != nil {
		panic(err)
	}
	return u
}
