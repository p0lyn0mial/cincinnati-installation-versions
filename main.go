package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	u, err := url.Parse("https://api.openshift.com/api/upgrades_info/graph")
	if err != nil {
		return nil, err
	}
	queryParams := u.Query()
	queryParams.Add("channel", channel)
	queryParams.Add("arch", arch)
	u.RawQuery = queryParams.Encode()

	resp, err := client.Get(u.String())
	if err != nil {
		return nil, fmt.Errorf("error fetching data from %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("error: status %d when fetching data from %s", resp.StatusCode, u)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response from %s: %w", u, err)
	}
	var graph CincinnatiGraph
	if err = json.Unmarshal(body, &graph); err != nil {
		return nil, fmt.Errorf("error parsing JSON from %s: %w", u, err)
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

// discoverReleases discovers new releases from the startChannels for the given arch.
// It returns a ReleasesByChannel, with keys as full channel names.
func discoverReleases(client *http.Client, startChannels []string, arch string) (ReleasesByChannel, error) {
	var prefixes []string
	var minVersion *semver.Version
	var queue []string
	queued := map[string]bool{}

	for _, startChannel := range startChannels {
		channelPrefix, channelVersionStr, err := splitChannel(startChannel)
		if err != nil {
			return nil, err
		}
		prefixes = append(prefixes, channelPrefix)

		channelVersion, err := semver.NewVersion(channelVersionStr)
		if err != nil {
			return nil, err
		}
		if minVersion == nil || channelVersion.LessThan(minVersion) {
			minVersion = channelVersion
		}

		queued[startChannel] = true
		queue = append(queue, startChannel)
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

		fmt.Printf("Fetching %s graph for channel: %s\n", arch, channel)
		graph, err := fetchGraph(client, channel, arch)
		if err != nil {
			return nil, fmt.Errorf("error fetching %s graph for channel %s: %w", arch, channel, err)
		}

		if _, ok := releasesByChannel[channel]; !ok {
			releasesByChannel[channel] = make(VersionReleases)
		}

		for _, node := range graph.Nodes {
			if node.Version != nil && node.Version.Compare(minVersion) >= 0 {
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
						if channelVer.Compare(minVersion) >= 0 && !processed[ch] && !queued[ch] {
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
	flag.Parse()

	client := &http.Client{}

	multiArchReleasesByChannel, err := discoverReleases(client, []string{*startChannel}, "multi")
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
