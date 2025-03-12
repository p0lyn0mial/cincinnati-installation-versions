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

func fetchGraph(client *http.Client, channel string) (*CincinnatiGraph, error) {
	url := fmt.Sprintf("https://api.openshift.com/api/upgrades_info/graph?channel=%s", channel)
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
		return nil, err
	}

	var graph CincinnatiGraph
	if err := json.Unmarshal(body, &graph); err != nil {
		return nil, fmt.Errorf("error parsing JSON: %w", err)
	}
	return &graph, nil
}

// extractSemVersionFromChannel extracts the version part from a channel name by removing the prefix
// and creates a semver.Version. For example, for "stable-4.16" with prefix "stable-" it returns semver version "4.16".
func extractSemVersionFromChannel(channel, prefix string) (*semver.Version, error) {
	trimmed := strings.TrimSpace(channel[len(prefix):])
	return semver.NewVersion(trimmed)
}

// dropPatch accepts a semver.Version and returns a new semver.Version dropping the patch part
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
		fmt.Printf("Error parsing min version %s: %v\n", *minVersion, err)
		return
	}

	minChannelVer, err := dropPatch(minVer)
	if err != nil {
		fmt.Printf("Error dropping patch from min version %s: %v\n", minVer.String(), err)
		return
	}

	client := &http.Client{}

	processedChannels := make(map[string]bool)
	nodesByChannel := make(map[string]map[string]CincinnatiNode)

	queue := []string{*startChannel}

	for len(queue) > 0 {
		currentChannel := queue[0]
		queue = queue[1:]

		if processedChannels[currentChannel] {
			continue
		}
		processedChannels[currentChannel] = true

		fmt.Printf("Fetching graph for channel: %s\n", currentChannel)
		graph, err := fetchGraph(client, currentChannel)
		if err != nil {
			fmt.Printf("Error fetching graph for channel %s: %v\n", currentChannel, err)
			continue
		}

		if _, exists := nodesByChannel[currentChannel]; !exists {
			nodesByChannel[currentChannel] = make(map[string]CincinnatiNode)
		}

		for _, node := range graph.Nodes {
			if node.Version != nil && node.Version.Compare(minVer) >= 0 {
				nodesByChannel[currentChannel][node.Version.String()] = node
			}

			if channels, ok := node.Metadata["io.openshift.upgrades.graph.release.channels"]; ok {
				for _, ch := range strings.Split(channels, ",") {
					ch = strings.TrimSpace(ch)
					// Check if the channel starts with one of the specified prefixes.
					for _, prefix := range prefixes {
						if strings.HasPrefix(ch, prefix) {
							// Only consider channels with version >= minimal version
							channelVer, err := extractSemVersionFromChannel(ch, prefix)
							if err != nil {
								fmt.Printf("Error parsing channel version from %s: %v\n", ch, err)
								continue
							}
							if channelVer.Compare(minChannelVer) >= 0 {
								if !processedChannels[ch] {
									fmt.Printf("Discovered new channel: %s\n", ch)
									queue = append(queue, ch)
								}
							}
						}
					}
				}
			}
		}
	}

	// Print sorted releases per channel.
	fmt.Println("\nAvailable releases (>= minimal version) per channel:")
	for channel, nodeMap := range nodesByChannel {
		nodes := make([]CincinnatiNode, 0, len(nodeMap))
		for _, node := range nodeMap {
			nodes = append(nodes, node)
		}
		sort.Slice(nodes, func(i, j int) bool {
			return nodes[i].Version.Compare(nodes[j].Version) < 0
		})
		fmt.Printf("Channel %s:\n", channel)
		for _, node := range nodes {
			fmt.Printf("  Version: %s, Payload: %s\n", node.Version.String(), node.Payload)
		}
	}

	// Aggregate and display unique releases per channel type.
	uniqueNodesByPrefix := make(map[string]map[string]CincinnatiNode)
	for _, prefix := range prefixes {
		uniqueNodesByPrefix[prefix] = make(map[string]CincinnatiNode)
		for channel, nodeMap := range nodesByChannel {
			if strings.HasPrefix(channel, prefix) {
				for version, node := range nodeMap {
					uniqueNodesByPrefix[prefix][version] = node
				}
			}
		}
	}

	fmt.Println("\nUnique releases per channel type:")
	for prefix, nodeMap := range uniqueNodesByPrefix {
		nodes := make([]CincinnatiNode, 0, len(nodeMap))
		for _, node := range nodeMap {
			nodes = append(nodes, node)
		}
		sort.Slice(nodes, func(i, j int) bool {
			return nodes[i].Version.Compare(nodes[j].Version) < 0
		})
		fmt.Printf("Channel type %s:\n", prefix)
		for _, node := range nodes {
			fmt.Printf("  Version: %s, Payload: %s\n", node.Version.String(), node.Payload)
		}
	}
}
