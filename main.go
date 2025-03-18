package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
)

type CincinnatiGraph struct {
	Nodes []CincinnatiNode `json:"nodes"`
}

type CincinnatiNode struct {
	Version  *semver.Version   `json:"version"`
	Payload  string            `json:"payload"`
	Metadata map[string]string `json:"metadata"`
}

type Release struct {
	Version  *semver.Version
	Channel  string
	Arch     string
	Payload  string
	Metadata map[string]string
}

type VersionReleases map[string]Release

type ReleasesByChannel map[string]VersionReleases

// fetchGraph fetches the upgrade graph for a given channel and architecture.
func fetchGraph(client *http.Client, channel, arch string) (*CincinnatiGraph, error) {
	url := fmt.Sprintf("https://api.openshift.com/api/upgrades_info/graph?channel=%s&arch=%s", channel, arch)
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("error fetching data from %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("error: status %d when fetching data from %s", resp.StatusCode, url)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response from %s: %w", url, err)
	}
	var graph CincinnatiGraph
	if err = json.Unmarshal(body, &graph); err != nil {
		return nil, fmt.Errorf("error parsing JSON from %s: %w", url, err)
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

// dropPatch returns a semver.Version containing only the major.minor parts.
func dropPatch(v *semver.Version) (*semver.Version, error) {
	newVersionStr := fmt.Sprintf("%v.%v", v.Major(), v.Minor())
	return semver.NewVersion(newVersionStr)
}

// discoverReleases discovers new releases from the startChannel and minimum acceptable version.
// It returns a ReleasesByChannel, with keys as full channel names.
func discoverReleases(client *http.Client, startChannel string, minVer, minChannelVer *semver.Version, prefixes []string, arch string) (ReleasesByChannel, error) {
	releasesByChannel := make(ReleasesByChannel)
	processed := make(map[string]bool)
	queued := map[string]bool{startChannel: true}
	queue := []string{startChannel}

	for len(queue) > 0 {
		channel := queue[0]
		queue = queue[1:]
		if processed[channel] {
			continue
		}
		processed[channel] = true

		fmt.Printf("Fetching %s graph for channel: %s\n", arch, channel)
		graph, err := fetchGraph(client, channel, arch)
		if err != nil {
			return nil, fmt.Errorf("error fetching %s graph for channel %s: %w", arch, channel, err)
		}

		if _, ok := releasesByChannel[channel]; !ok {
			releasesByChannel[channel] = make(VersionReleases)
		}

		for _, node := range graph.Nodes {
			if node.Version != nil && node.Version.Compare(minVer) >= 0 {
				verStr := node.Version.String()
				r := Release{
					Version:  node.Version,
					Channel:  channel,
					Arch:     arch,
					Payload:  node.Payload,
					Metadata: node.Metadata,
				}
				releasesByChannel[channel][verStr] = r
			}

			metaChannels, ok := node.Metadata["io.openshift.upgrades.graph.release.channels"]
			if !ok {
				continue
			}
			for _, ch := range strings.Split(metaChannels, ",") {
				ch = strings.TrimSpace(ch)
				for _, prefix := range prefixes {
					if strings.HasPrefix(ch, prefix) {
						channelVer, err := extractSemVersionFromChannel(ch, prefix)
						if err != nil {
							return nil, fmt.Errorf("error parsing channel version from %s: %w", ch, err)
						}
						if channelVer.Compare(minChannelVer) >= 0 && !processed[ch] && !queued[ch] {
							fmt.Printf("Discovered new channel: %s\n", ch)
							queue = append(queue, ch)
							queued[ch] = true
						}
					}
				}
			}
		}
	}

	return releasesByChannel, nil
}

func main() {
	startChannel := flag.String("channel", "fast-4.16", "Starting channel (e.g. stable-4.16)")
	minVersion := flag.String("min", "4.16.0", "Minimal version (e.g. 4.16.0 or 4.16.1)")
	prefixesArg := flag.String("prefixes", "fast-,stable-", "Channel prefixes separated by comma (e.g. fast-,stable-)")
	flag.Parse()
	prefixes := strings.Split(*prefixesArg, ",")

	minVer, err := semver.NewVersion(*minVersion)
	if err != nil {
		fmt.Printf("Error parsing minimal version %s: %v\n", *minVersion, err)
		return
	}
	minChannelVer, err := dropPatch(minVer)
	if err != nil {
		fmt.Printf("Error dropping patch from minimal version %s: %v\n", minVer.String(), err)
		return
	}
	client := &http.Client{}

	multiArchReleasesByChannel, err := discoverReleases(client, *startChannel, minVer, minChannelVer, prefixes, "multi")
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
			fmt.Printf("  Version: %s, Channel: %s, Payload: %s, Arch: %s\n", ver, release.Channel, release.Payload, release.Arch)
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
			if _, exists := aggregated[group][version]; !exists {
				aggregated[group][version] = release
			}
		}
	}
	return aggregated
}
