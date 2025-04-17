package cincinnaticlient

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/Masterminds/semver/v3"
)

type Graph struct {
	Nodes            []Node             `json:"nodes"`
	Edges            [][]int            `json:"edges"`
	ConditionalEdges []ConditionalEdges `json:"conditionalEdges"`
}

type Node struct {
	Version  *semver.Version   `json:"version"`
	Payload  string            `json:"payload"`
	Metadata map[string]string `json:"metadata"`
}

type Risk struct {
	Name string `json:"name"`
}

type ConditionalEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type ConditionalEdges struct {
	Edges []ConditionalEdge `json:"edges"`
	Risks []Risk            `json:"risks"`
}

type Release struct {
	Version           string
	Channel           string
	Arch              string
	Payload           string
	AvailableUpgrades []string
}

type VersionReleases map[string]Release

type ReleasesByChannel map[string]VersionReleases

type Client struct {
	httpClient *http.Client
}

func New(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		httpClient: httpClient,
	}
}

func (c *Client) DiscoverReleases(graphURL *url.URL, startChannel string, arch string, allowedConditionalEdgeRisks []string) (ReleasesByChannel, error) {
	return discoverReleases(c.httpClient, graphURL, startChannel, arch, allowedConditionalEdgeRisks)
}

// fetchGraph fetches the upgrade graph for a given channel and architecture.
func fetchGraph(client *http.Client, u *url.URL, channel, arch string) (*Graph, error) {
	if u == nil {
		return nil, fmt.Errorf("cincinnati graph URL is required")
	}
	modURL := *u
	queryParams := modURL.Query()
	queryParams.Add("channel", channel)
	queryParams.Add("arch", arch)
	modURL.RawQuery = queryParams.Encode()

	req, err := http.NewRequest("GET", modURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request for %s: %w", modURL.String(), err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error fetching data from %s: %w", modURL.String(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("error: status %d when fetching data from %s", resp.StatusCode, modURL.String())
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response from %s: %w", modURL.String(), err)
	}
	var graph Graph
	if err = json.Unmarshal(body, &graph); err != nil {
		return nil, fmt.Errorf("error parsing JSON from %s: %w", modURL.String(), err)
	}
	return &graph, nil
}

// extractSemVersionFromChannel removes the given prefix from a channel name
// and creates a semver.Version. For example, for "stable-4.16" with prefix "stable-"
// it returns a semver version for "4.16".
func extractSemVersionFromChannel(channel, prefix string) (*semver.Version, error) {
	trimmed := strings.TrimSpace(channel[len(prefix):])
	return semver.NewVersion(trimmed)
}

// splitChannel splits the input string into a prefix (including the hyphen)
// and the version part. It assumes that the input always contains a hyphen.
func splitChannel(channel string) (string, string, error) {
	idx := strings.Index(channel, "-")
	// If the hyphen is not found, return an empty prefix and the original input as version.
	if idx == -1 {
		return "", channel, fmt.Errorf("invalid channel format: %s", channel)
	}
	prefix := channel[:idx+1]
	version := channel[idx+1:]
	return prefix, version, nil
}

// isValidVersion checks if the given version is not nil and >= minVersion
func isValidVersion(v *semver.Version, minVersion *semver.Version) bool {
	return v != nil && v.Compare(minVersion) >= 0
}

// processEdges process the cincinnati graph edges and updates AvailableUpgrades
func processEdges(graph *Graph, minVersion *semver.Version, releases VersionReleases) error {
	for idx, edge := range graph.Edges {
		if len(edge) < 2 {
			return fmt.Errorf("invalid edge format: expected 2 ints, got: %v", edge)
		}
		fromIdx, toIdx := edge[0], edge[1]
		if fromIdx < 0 || fromIdx >= len(graph.Nodes) || toIdx < 0 || toIdx >= len(graph.Nodes) {
			return fmt.Errorf("invalid edge indices: %v at index: %d", edge, idx)
		}
		fromNode := graph.Nodes[fromIdx]
		toNode := graph.Nodes[toIdx]
		if isValidVersion(fromNode.Version, minVersion) && isValidVersion(toNode.Version, minVersion) {
			fromVerStr := fromNode.Version.String()
			toVerStr := toNode.Version.String()
			if r, ok := releases[fromVerStr]; ok {
				if !slices.Contains(r.AvailableUpgrades, toVerStr) {
					r.AvailableUpgrades = append(r.AvailableUpgrades, toVerStr)
					releases[fromVerStr] = r
				}
			}
		}
	}
	return nil
}

// processConditionalEdges processes conditional edges.
// For each conditional edge group, it checks that every risk in the group is accepted.
// Only if all risks are accepted, the function adds the upgrade to the AvailableUpgrades.
func processConditionalEdges(conditionalEdges []ConditionalEdges, allowedConditionalEdgeRisks []string, releases VersionReleases) {
	for _, group := range conditionalEdges {
		allAccepted := true
		for _, risk := range group.Risks {
			if !slices.Contains(allowedConditionalEdgeRisks, risk.Name) {
				allAccepted = false
				break
			}
		}
		if !allAccepted {
			continue
		}

		for _, edge := range group.Edges {
			fromVerStr := edge.From
			toVerStr := edge.To
			if r, ok := releases[fromVerStr]; ok {
				if !slices.Contains(r.AvailableUpgrades, toVerStr) {
					r.AvailableUpgrades = append(r.AvailableUpgrades, toVerStr)
					releases[fromVerStr] = r
				}
			}
		}
	}
}

// createRelease simply creates a release from the given node.
func createRelease(node Node, channel, arch string, minVersion *semver.Version) (Release, bool) {
	if !isValidVersion(node.Version, minVersion) {
		return Release{}, false
	}
	r := Release{
		Version: node.Version.String(),
		Channel: channel,
		Arch:    arch,
		Payload: node.Payload,
	}
	return r, true
}

// discoverNewChannels checks node's metadata and returns new channels that match the condition.
func discoverNewChannels(node Node, startChannelPrefix string, minVersion *semver.Version) []string {
	var newCh []string
	meta, ok := node.Metadata["io.openshift.upgrades.graph.release.channels"]
	if !ok {
		return newCh
	}
	for _, ch := range strings.Split(meta, ",") {
		ch = strings.TrimSpace(ch)
		if strings.HasPrefix(ch, startChannelPrefix) {
			channelVer, err := extractSemVersionFromChannel(ch, startChannelPrefix)
			if err != nil {
				continue
			}
			if isValidVersion(channelVer, minVersion) {
				newCh = append(newCh, ch)
			}
		}
	}
	return newCh
}

// discoverReleases discovers new releases from the startChannels for the given arch.
// It returns a ReleasesByChannel, with keys as full channel names.
func discoverReleases(client *http.Client, graphURL *url.URL, startChannel string, arch string, allowedConditionalEdgeRisks []string) (ReleasesByChannel, error) {
	startChannelPrefix, startChannelVersionStr, err := splitChannel(startChannel)
	if err != nil {
		return nil, err
	}

	startChannelVersion, err := semver.NewVersion(startChannelVersionStr)
	if err != nil {
		return nil, err
	}
	minVersion := startChannelVersion

	queue := []string{startChannel}
	queued := map[string]bool{
		startChannel: true,
	}

	releasesByChannel := make(ReleasesByChannel)
	processed := make(map[string]bool)

	for len(queue) > 0 {
		channel := queue[0]
		queue = queue[1:]
		if processed[channel] {
			continue
		}
		processed[channel] = true

		graph, err := fetchGraph(client, graphURL, channel, arch)
		if err != nil {
			return nil, fmt.Errorf("error fetching %s graph for channel %s: %w", arch, channel, err)
		}

		if _, ok := releasesByChannel[channel]; !ok {
			releasesByChannel[channel] = make(VersionReleases)
		}

		for _, node := range graph.Nodes {
			if r, found := createRelease(node, channel, arch, minVersion); found {
				releasesByChannel[channel][r.Version] = r
			}
			newChannels := discoverNewChannels(node, startChannelPrefix, minVersion)
			for _, ch := range newChannels {
				if !queued[ch] && !processed[ch] {
					queue = append(queue, ch)
					queued[ch] = true
				}
			}
		}
		if err = processEdges(graph, minVersion, releasesByChannel[channel]); err != nil {
			return nil, err
		}
		processConditionalEdges(graph.ConditionalEdges, allowedConditionalEdgeRisks, releasesByChannel[channel])
	}
	return releasesByChannel, nil
}
