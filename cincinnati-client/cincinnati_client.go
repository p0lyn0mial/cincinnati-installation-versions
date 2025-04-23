package cincinnaticlient

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/go-version"
)

// Graph represents the upgrade graph returned by Cincinnati.
// It contains nodes (versions), unconditional edges, and conditional edge groups.
type Graph struct {
	Nodes            []Node             `json:"nodes"`
	Edges            [][]int            `json:"edges"`
	ConditionalEdges []ConditionalEdges `json:"conditionalEdges"`
}

// Node describes a single graph node: its semantic version, payload identifier,
// and any associated metadata.
type Node struct {
	Version  *version.Version  `json:"version"`
	Payload  string            `json:"payload"`
	Metadata map[string]string `json:"metadata"`
}

// Risk names a single risk associated with a conditional edge.
type Risk struct {
	Name string `json:"name"`
}

// ConditionalEdge represents one upgrade edge from → to,
// which is only valid if its risks are accepted.
type ConditionalEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ConditionalEdges groups multiple ConditionalEdge entries under the same risks.
// If all risks are accepted, these edges can be applied.
type ConditionalEdges struct {
	Edges []ConditionalEdge `json:"edges"`
	Risks []Risk            `json:"risks"`
}

// Release represents a discovered release for a specific architecture.
// It includes the version, payload, and available upgrade targets.
type Release struct {
	Version           string
	Arch              string
	Payload           string
	AvailableUpgrades []string
}

// SortAvailableUpgrades orders AvailableUpgrades in ascending semantic-version order.
// It returns an error if any entry is not a valid semantic version.
//
// NOTE: Every version string must already be a valid semver, since they
//
//	are converted to semver objects when reading the cincinnati graph.
func (r Release) SortAvailableUpgrades() error {
	for i, upgrade := range r.AvailableUpgrades {
		_, err := version.NewVersion(upgrade)
		if err != nil {
			return fmt.Errorf("%s: invalid semantic version in AvailableUpgrades[%d]=%q: %w", r.Version, i, upgrade, err)
		}
	}

	sort.Slice(r.AvailableUpgrades, func(i, j int) bool {
		v1, _ := version.NewVersion(r.AvailableUpgrades[i])
		v2, _ := version.NewVersion(r.AvailableUpgrades[j])
		return v1.Compare(v2) < 0
	})
	return nil
}

// VersionReleases maps a version string to its Release object.
type VersionReleases map[string]Release

// ReleasesByChannel maps a channel name to its set of VersionReleases.
type ReleasesByChannel map[string]VersionReleases

// Client is the Cincinnati API client that fetches graphs
// and computes available releases.
type Client struct {
	httpClient *http.Client
}

// New returns a Client using the given http.Client.
// If httpClient is nil, http.DefaultClient is used.
func New(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		httpClient: httpClient,
	}
}

// DiscoverReleases discovers new releases from the startChannels for the given arch.
// It returns a ReleasesByChannel, with keys as full channel names.
func (c *Client) DiscoverReleases(graphURL *url.URL, startChannel string, arch string, allowedConditionalEdgeRisks []string) (ReleasesByChannel, error) {
	startChannelPrefix, startChannelVersionStr, err := c.splitChannel(startChannel)
	if err != nil {
		return nil, err
	}

	startChannelVersion, err := version.NewVersion(startChannelVersionStr)
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

		graph, err := c.fetchGraph(graphURL, channel, arch)
		if err != nil {
			return nil, fmt.Errorf("error fetching %s graph for channel %s: %w", arch, channel, err)
		}

		if _, ok := releasesByChannel[channel]; !ok {
			releasesByChannel[channel] = make(VersionReleases)
		}

		for _, node := range graph.Nodes {
			if r, found := c.createRelease(node, arch, minVersion); found {
				releasesByChannel[channel][r.Version] = r
			}
			newChannels := c.discoverNewChannels(node, startChannelPrefix, minVersion)
			for _, ch := range newChannels {
				if !queued[ch] && !processed[ch] {
					queue = append(queue, ch)
					queued[ch] = true
				}
			}
		}
		if err = c.processEdges(graph, releasesByChannel[channel]); err != nil {
			return nil, err
		}
		c.processConditionalEdges(graph.ConditionalEdges, allowedConditionalEdgeRisks, releasesByChannel[channel])
	}
	return releasesByChannel, nil
}

// fetchGraph fetches the upgrade graph for a given channel and architecture.
func (c *Client) fetchGraph(u *url.URL, channel, arch string) (*Graph, error) {
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

	resp, err := c.httpClient.Do(req)
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
func (c *Client) extractSemVersionFromChannel(channel, prefix string) (*version.Version, error) {
	trimmed := strings.TrimSpace(channel[len(prefix):])
	return version.NewVersion(trimmed)
}

// splitChannel splits the input string into a prefix (including the hyphen)
// and the version part. It assumes that the input always contains a hyphen.
func (c *Client) splitChannel(channel string) (string, string, error) {
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
func (c *Client) isValidVersion(v *version.Version, minVersion *version.Version) bool {
	return v != nil && v.Compare(minVersion) >= 0
}

// processEdges process the cincinnati graph edges and updates AvailableUpgrades
func (c *Client) processEdges(graph *Graph, releases VersionReleases) error {
	for idx, edge := range graph.Edges {
		if len(edge) < 2 {
			return fmt.Errorf("invalid edge format: expected 2 ints, got: %v", edge)
		}
		fromIdx, toIdx := edge[0], edge[1]
		if fromIdx < 0 || fromIdx >= len(graph.Nodes) || toIdx < 0 || toIdx >= len(graph.Nodes) {
			return fmt.Errorf("invalid edge indices: %v at index: %d", edge, idx)
		}
		fromVerStr := graph.Nodes[fromIdx].Version.String()
		if r, ok := releases[fromVerStr]; ok {
			toVerStr := graph.Nodes[toIdx].Version.String()
			if !slices.Contains(r.AvailableUpgrades, toVerStr) {
				r.AvailableUpgrades = append(r.AvailableUpgrades, toVerStr)
				releases[fromVerStr] = r
			}
		}
	}
	return nil
}

// processConditionalEdges processes conditional edges.
// For each conditional edge group, it checks that every risk in the group is accepted.
// Only if all risks are accepted, the function adds the upgrade to the AvailableUpgrades.
func (c *Client) processConditionalEdges(conditionalEdges []ConditionalEdges, allowedConditionalEdgeRisks []string, releases VersionReleases) {
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
func (c *Client) createRelease(node Node, arch string, minVersion *version.Version) (Release, bool) {
	if !c.isValidVersion(node.Version, minVersion) {
		return Release{}, false
	}
	r := Release{
		Version: node.Version.String(),
		Arch:    arch,
		Payload: node.Payload,
	}
	return r, true
}

// discoverNewChannels checks node's metadata and returns new channels that match the condition.
func (c *Client) discoverNewChannels(node Node, startChannelPrefix string, minVersion *version.Version) []string {
	var newCh []string
	meta, ok := node.Metadata["io.openshift.upgrades.graph.release.channels"]
	if !ok {
		return newCh
	}
	for _, ch := range strings.Split(meta, ",") {
		ch = strings.TrimSpace(ch)
		if strings.HasPrefix(ch, startChannelPrefix) {
			channelVer, err := c.extractSemVersionFromChannel(ch, startChannelPrefix)
			if err != nil {
				continue
			}
			if c.isValidVersion(channelVer, minVersion) {
				newCh = append(newCh, ch)
			}
		}
	}
	return newCh
}
