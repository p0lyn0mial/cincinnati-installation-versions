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

type ChannelReleases map[string][]Release

type ReleasesByChannel map[string]ChannelReleases

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
func discoverReleases(client *http.Client, startChannel string, minVer, minChannelVer *semver.Version, prefixes []string, arch string) ReleasesByChannel {
	releasesAgg := make(ReleasesByChannel)
	processedChannels := make(map[string]bool)
	queuedChannels := make(map[string]bool)
	queue := []string{startChannel}
	queuedChannels[startChannel] = true

	for len(queue) > 0 {
		currentChannel := queue[0]
		queue = queue[1:]
		if processedChannels[currentChannel] {
			continue
		}
		processedChannels[currentChannel] = true

		fmt.Printf("Fetching %s graph for channel: %s\n", arch, currentChannel)
		graph, err := fetchGraph(client, currentChannel, arch)
		if err != nil {
			fmt.Printf("Error fetching %s graph for channel %s: %v\n", arch, currentChannel, err)
			continue
		}
		if _, exists := releasesAgg[currentChannel]; !exists {
			releasesAgg[currentChannel] = make(ChannelReleases)
		}
		for _, node := range graph.Nodes {
			if node.Version != nil && node.Version.Compare(minVer) >= 0 {
				verStr := node.Version.String()
				r := Release{
					Version:  node.Version,
					Channel:  currentChannel,
					Arch:     arch,
					Payload:  node.Payload,
					Metadata: node.Metadata,
				}
				releasesAgg[currentChannel][verStr] = append(releasesAgg[currentChannel][verStr], r)
			}
			if channels, ok := node.Metadata["io.openshift.upgrades.graph.release.channels"]; ok {
				for _, ch := range strings.Split(channels, ",") {
					ch = strings.TrimSpace(ch)
					for _, prefix := range prefixes {
						if strings.HasPrefix(ch, prefix) {
							channelVer, err := extractSemVersionFromChannel(ch, prefix)
							if err != nil {
								fmt.Printf("Error parsing channel version from %s: %v\n", ch, err)
								continue
							}
							if channelVer.Compare(minChannelVer) >= 0 {
								if !processedChannels[ch] && !queuedChannels[ch] {
									fmt.Printf("Discovered new channel: %s\n", ch)
									queue = append(queue, ch)
									queuedChannels[ch] = true
								}
							}
						}
					}
				}
			}
		}
	}
	return releasesAgg
}

// fetchReleases fetches releases for a given architecture for the provided slice of channels.
// It returns a ReleasesByChannel with keys as the full channel names.
func fetchReleases(client *http.Client, channels []string, minVer *semver.Version, arch string) ReleasesByChannel {
	releasesAgg := make(ReleasesByChannel)
	for _, channel := range channels {
		fmt.Printf("Fetching %s graph for channel: %s\n", arch, channel)
		graph, err := fetchGraph(client, channel, arch)
		if err != nil {
			fmt.Printf("Error fetching %s graph for channel %s: %v\n", arch, channel, err)
			continue
		}
		if _, exists := releasesAgg[channel]; !exists {
			releasesAgg[channel] = make(ChannelReleases)
		}
		for _, node := range graph.Nodes {
			if node.Version == nil || node.Version.Compare(minVer) < 0 {
				continue
			}
			verStr := node.Version.String()
			r := Release{
				Version:  node.Version,
				Channel:  channel,
				Arch:     arch,
				Payload:  node.Payload,
				Metadata: node.Metadata,
			}
			releasesAgg[channel][verStr] = append(releasesAgg[channel][verStr], r)
		}
	}
	return releasesAgg
}

// mergeReleases merges two ReleasesByChannel maps (e.g. for AMD64 and multi)
// into an aggregated map where the key is the version string and the value is a slice of Release.
func mergeReleases(amd64Releases, multiReleases ReleasesByChannel) map[string][]Release {
	merged := make(map[string][]Release)

	for _, chReleases := range amd64Releases {
		for ver, releases := range chReleases {
			merged[ver] = append(merged[ver], releases...)
		}
	}

	for _, chReleases := range multiReleases {
		for ver, releases := range chReleases {
			merged[ver] = append(merged[ver], releases...)
		}
	}
	return merged
}

func main() {
	startChannel := flag.String("channel", "stable-4.16", "Starting channel (e.g. stable-4.16)")
	minVersion := flag.String("min", "4.16.0", "Minimal version (e.g. 4.16.0 or 4.16.1)")
	prefixesArg := flag.String("prefixes", "fast-,stable-", "Channel prefixes separated by comma")
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

	// Step 1: Discover releases for "amd64".
	amd64ReleasesRaw := discoverReleases(client, *startChannel, minVer, minChannelVer, prefixes, "amd64")

	fmt.Println("\nDiscovered AMD64 releases:")
	for channel, relMap := range amd64ReleasesRaw {
		fmt.Printf("Channel: %s\n", channel)
		versions := make([]string, 0, len(relMap))
		for ver := range relMap {
			versions = append(versions, ver)
		}
		sort.Slice(versions, func(i, j int) bool {
			v1, _ := semver.NewVersion(versions[i])
			v2, _ := semver.NewVersion(versions[j])
			return v1.Compare(v2) < 0
		})
		for _, ver := range versions {
			for _, r := range relMap[ver] {
				fmt.Printf("  Version: %s, Payload: %s\n", r.Version.String(), r.Payload)
			}
		}
	}

	// Step 2: Fetch releases for "multi" using the discovered channels.
	var discoveredChannels []string
	for channel := range amd64ReleasesRaw {
		discoveredChannels = append(discoveredChannels, channel)
	}
	multiReleases := fetchReleases(client, discoveredChannels, minVer, "multi")

	fmt.Println("\nDiscovered multi releases:")
	for channel, relMap := range multiReleases {
		fmt.Printf("Channel: %s\n", channel)
		versions := make([]string, 0, len(relMap))
		for ver := range relMap {
			versions = append(versions, ver)
		}
		sort.Slice(versions, func(i, j int) bool {
			v1, _ := semver.NewVersion(versions[i])
			v2, _ := semver.NewVersion(versions[j])
			return v1.Compare(v2) < 0
		})
		for _, ver := range versions {
			for _, r := range relMap[ver] {
				fmt.Printf("  Version: %s, Payload: %s\n", r.Version.String(), r.Payload)
			}
		}
	}

	// Step 3: Merge AMD64 and multi releases by version.
	aggregatedReleases := mergeReleases(amd64ReleasesRaw, multiReleases)

	fmt.Println("\nAggregated releases by version (AMD64 + multi):")
	versions := make([]string, 0, len(aggregatedReleases))
	for ver := range aggregatedReleases {
		versions = append(versions, ver)
	}
	sort.Slice(versions, func(i, j int) bool {
		v1, _ := semver.NewVersion(versions[i])
		v2, _ := semver.NewVersion(versions[j])
		return v1.Compare(v2) < 0
	})
	for _, ver := range versions {
		releases := aggregatedReleases[ver]
		sort.Slice(releases, func(i, j int) bool {
			return releases[i].Channel < releases[j].Channel
		})
		fmt.Printf("Version: %s\n", ver)
		for _, r := range releases {
			fmt.Printf("  Channel: %s, Arch: %s, Payload: %s\n", r.Channel, r.Arch, r.Payload)
		}
		multiFound := false
		for _, r := range releases {
			if r.Arch == "multi" {
				multiFound = true
				break
			}
		}
		if !multiFound {
			fmt.Printf("  (No multi release found for version %s)\n", ver)
		}
	}
}
