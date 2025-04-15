package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
)

type CincinnatiGraph struct {
	Nodes []CincinnatiNode `json:"nodes"`
	Edges [][]int          `json:"edges"`
}

type CincinnatiNode struct {
	Version  *semver.Version   `json:"version"`
	Payload  string            `json:"payload"`
	Metadata map[string]string `json:"metadata"`
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

// fetchGraph fetches the upgrade graph for a given channel and architecture.
func fetchGraph(client *http.Client, u *url.URL, channel, arch string) (*CincinnatiGraph, error) {
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
	var graph CincinnatiGraph
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
func processEdges(graph *CincinnatiGraph, minVersion *semver.Version, releases VersionReleases) error {
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

// discoverReleases discovers new releases from the startChannels for the given arch.
// It returns a ReleasesByChannel, with keys as full channel names.
func discoverReleases(client *http.Client, graphURL *url.URL, startChannel string, arch string) (ReleasesByChannel, error) {
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
			if isValidVersion(node.Version, minVersion) {
				verStr := node.Version.String()
				r := Release{
					Version: node.Version.String(),
					Channel: channel,
					Arch:    arch,
					Payload: node.Payload,
				}
				releasesByChannel[channel][verStr] = r
			}

			metaChannels, ok := node.Metadata["io.openshift.upgrades.graph.release.channels"]
			if !ok {
				continue
			}
			for _, ch := range strings.Split(metaChannels, ",") {
				ch = strings.TrimSpace(ch)
				if strings.HasPrefix(ch, startChannelPrefix) {
					channelVer, err := extractSemVersionFromChannel(ch, startChannelPrefix)
					if err != nil {
						return nil, fmt.Errorf("error parsing channel version from %s: %w", ch, err)
					}
					if channelVer.Compare(minVersion) >= 0 && !processed[ch] && !queued[ch] {
						queue = append(queue, ch)
						queued[ch] = true
					}
				}
			}
		}

		if err = processEdges(graph, minVersion, releasesByChannel[channel]); err != nil {
			return nil, err
		}
	}

	return releasesByChannel, nil
}

func main() {
	startChannel := flag.String("channel", "fast-4.16", "Starting channel (e.g. stable-4.16)")
	flag.Parse()

	u, err := url.Parse("https://api.openshift.com/api/upgrades_info/graph")
	if err != nil {
		fmt.Printf("error parsing URL: %s\n", err)
		return
	}

	client := &http.Client{}
	multiArchReleasesByChannel, err := discoverReleases(client, u, *startChannel, "multi")
	if err != nil {
		fmt.Printf("error discovering releases from %s: %v\n", *startChannel, err)
		return
	}

	aggregatedMultiArchReleasesByChannelGroup := aggregateReleasesByChannelGroup(multiArchReleasesByChannel)
	fmt.Println("\nAggregated releases by channel group (prefix) with unique versions:")
	for group, versionsMap := range aggregatedMultiArchReleasesByChannelGroup {
		fmt.Printf("Group: %s\n", group)
		versions := make([]string, 0, len(versionsMap))
		for ver := range versionsMap {
			versions = append(versions, ver)
		}
		sort.Slice(versions, func(i, j int) bool {
			v1, _ := semver.NewVersion(versions[i])
			v2, _ := semver.NewVersion(versions[j])
			return v1.Compare(v2) < 0
		})
		for _, ver := range versions {
			release := versionsMap[ver]
			sort.Slice(release.AvailableUpgrades, func(i, j int) bool {
				v1, _ := semver.NewVersion(release.AvailableUpgrades[i])
				v2, _ := semver.NewVersion(release.AvailableUpgrades[j])
				return v1.Compare(v2) < 0
			})
			fmt.Printf("  Version: %s, Channel: %s, Payload: %s, Arch: %s, AvailableUpgrades: %s\n", ver, release.Channel, release.Payload, release.Arch, release.AvailableUpgrades)
		}
	}
}

func aggregateReleasesByChannelGroup(releasesByChannel ReleasesByChannel) ReleasesByChannel {
	aggregated := make(ReleasesByChannel)
	for channel, versionMap := range releasesByChannel {
		group := channel
		if idx := strings.Index(channel, "-"); idx != -1 {
			group = channel[:idx]
		}
		if aggregated[group] == nil {
			aggregated[group] = make(VersionReleases)
		}
		for version, release := range versionMap {
			if existing, exists := aggregated[group][version]; exists {
				for _, up := range release.AvailableUpgrades {
					if !slices.Contains(existing.AvailableUpgrades, up) {
						existing.AvailableUpgrades = append(existing.AvailableUpgrades, up)
					}
				}
				aggregated[group][version] = existing
			} else {
				aggregated[group][version] = release
			}
		}
	}
	return aggregated
}
