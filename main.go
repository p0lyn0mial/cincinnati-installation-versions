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

type MultiRelease struct {
	Version  string
	Releases []Release
}

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
	if err := json.Unmarshal(body, &graph); err != nil {
		return nil, fmt.Errorf("error parsing JSON from %s: %w", url, err)
	}
	return &graph, nil
}

// extractSemVersionFromChannel removes the prefix from a channel name and creates a semver.Version.
// For example, for "stable-4.16" with prefix "stable-" it returns a semver version for "4.16".
func extractSemVersionFromChannel(channel, prefix string) (*semver.Version, error) {
	trimmed := strings.TrimSpace(channel[len(prefix):])
	return semver.NewVersion(trimmed)
}

// dropPatch accepts a semver.Version and returns a new semver.Version containing only major.minor.
func dropPatch(v *semver.Version) (*semver.Version, error) {
	newVersionStr := fmt.Sprintf("%v.%v", v.Major(), v.Minor())
	return semver.NewVersion(newVersionStr)
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

	// Step 1: Aggregate AMD64 releases.
	// amd64Releases: map[channel] -> map[version string] -> []Release.
	amd64Releases := make(map[string]map[string][]Release)
	processedChannels := make(map[string]bool)
	queuedChannels := make(map[string]bool)
	queue := []string{*startChannel}
	queuedChannels[*startChannel] = true

	for len(queue) > 0 {
		currentChannel := queue[0]
		queue = queue[1:]
		if processedChannels[currentChannel] {
			continue
		}
		processedChannels[currentChannel] = true

		fmt.Printf("Fetching AMD64 graph for channel: %s\n", currentChannel)
		graph, err := fetchGraph(client, currentChannel, "amd64")
		if err != nil {
			fmt.Printf("Error fetching AMD64 graph for channel %s: %v\n", currentChannel, err)
			continue
		}
		if _, exists := amd64Releases[currentChannel]; !exists {
			amd64Releases[currentChannel] = make(map[string][]Release)
		}
		for _, node := range graph.Nodes {
			if node.Version != nil && node.Version.Compare(minVer) >= 0 {
				verStr := node.Version.String()
				r := Release{
					Version:  node.Version,
					Channel:  currentChannel,
					Arch:     "amd64",
					Payload:  node.Payload,
					Metadata: node.Metadata,
				}
				amd64Releases[currentChannel][verStr] = append(amd64Releases[currentChannel][verStr], r)
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

	// Group AMD64 releases by channel type (prefix).
	// uniqueAMD64: map[prefix] -> map[version string] -> []Release.
	uniqueAMD64 := make(map[string]map[string][]Release)
	for _, prefix := range prefixes {
		uniqueAMD64[prefix] = make(map[string][]Release)
		for channel, relMap := range amd64Releases {
			if strings.HasPrefix(channel, prefix) {
				for ver, releases := range relMap {
					uniqueAMD64[prefix][ver] = append(uniqueAMD64[prefix][ver], releases...)
				}
			}
		}
	}

	// Display AMD64 results grouped by channel type.
	fmt.Println("\nAvailable AMD64 releases per channel type:")
	for prefix, relMap := range uniqueAMD64 {
		versions := make([]string, 0, len(relMap))
		for ver := range relMap {
			versions = append(versions, ver)
		}
		sort.Slice(versions, func(i, j int) bool {
			v1, _ := semver.NewVersion(versions[i])
			v2, _ := semver.NewVersion(versions[j])
			return v1.Compare(v2) < 0
		})
		fmt.Printf("Channel type %s:\n", prefix)
		for _, ver := range versions {
			for _, r := range relMap[ver] {
				fmt.Printf("  Version: %s, Channel: %s, Payload: %s\n", r.Version.String(), r.Channel, r.Payload)
			}
		}
	}

	// Step 2: For each prefix, separately aggregate multi releases.
	// uniqueMulti: map[prefix] -> map[version string] -> []Release (for multi).
	uniqueMulti := make(map[string]map[string][]Release)
	for _, prefix := range prefixes {
		uniqueMulti[prefix] = make(map[string][]Release)
		for channel := range amd64Releases {
			if !strings.HasPrefix(channel, prefix) {
				continue
			}
			fmt.Printf("Fetching multi graph for channel: %s\n", channel)
			graphMulti, err := fetchGraph(client, channel, "multi")
			if err != nil {
				fmt.Printf("Error fetching multi graph for channel %s: %v\n", channel, err)
				continue
			}
			for _, node := range graphMulti.Nodes {
				if node.Version == nil || node.Version.Compare(minVer) < 0 {
					continue
				}
				verStr := node.Version.String()
				r := Release{
					Version:  node.Version,
					Channel:  channel,
					Arch:     "multi",
					Payload:  node.Payload,
					Metadata: node.Metadata,
				}
				uniqueMulti[prefix][verStr] = append(uniqueMulti[prefix][verStr], r)
			}
		}
	}

	// Step 3: Merge AMD64 and multi releases by version for each prefix.
	// aggregatedReleases: map[prefix] -> map[version string] -> MultiRelease.
	aggregatedReleases := make(map[string]map[string]MultiRelease)
	for _, prefix := range prefixes {
		aggregatedReleases[prefix] = make(map[string]MultiRelease)
		// First, add AMD64 releases.
		for ver, amd64ReleasesSlice := range uniqueAMD64[prefix] {
			mr := MultiRelease{
				Version:  ver,
				Releases: []Release{},
			}
			mr.Releases = append(mr.Releases, amd64ReleasesSlice...)
			aggregatedReleases[prefix][ver] = mr
		}
		// Then, add multi releases.
		for ver, multiReleasesSlice := range uniqueMulti[prefix] {
			mr, exists := aggregatedReleases[prefix][ver]
			if !exists {
				mr = MultiRelease{
					Version:  ver,
					Releases: []Release{},
				}
			}
			mr.Releases = append(mr.Releases, multiReleasesSlice...)
			aggregatedReleases[prefix][ver] = mr
		}
	}

	// Step 4: Print final aggregated results sorted by version and channel.
	fmt.Println("\nAggregated releases per channel type (AMD64 + multi):")
	for _, prefix := range prefixes {
		releasesMap, exists := aggregatedReleases[prefix]
		if !exists {
			continue
		}
		versions := make([]string, 0, len(releasesMap))
		for ver := range releasesMap {
			versions = append(versions, ver)
		}
		// Sort versions using semver.
		sort.Slice(versions, func(i, j int) bool {
			v1, _ := semver.NewVersion(versions[i])
			v2, _ := semver.NewVersion(versions[j])
			return v1.Compare(v2) < 0
		})
		fmt.Printf("Channel type %s:\n", prefix)
		for _, ver := range versions {
			mr := releasesMap[ver]
			// Sort the releases slice by channel.
			sort.Slice(mr.Releases, func(i, j int) bool {
				return mr.Releases[i].Channel < mr.Releases[j].Channel
			})
			fmt.Printf("  Version: %s\n", mr.Version)
			for _, r := range mr.Releases {
				fmt.Printf("    Channel: %s, Arch: %s, Payload: %s\n", r.Channel, r.Arch, r.Payload)
			}
			multiFound := false
			for _, r := range mr.Releases {
				if r.Arch == "multi" {
					multiFound = true
					break
				}
			}
			if !multiFound {
				fmt.Printf("    (No multi release found for version %s)\n", mr.Version)
			}
		}
	}
}
