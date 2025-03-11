package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

type CincinnatiGraph struct {
	Nodes []CincinnatiNode `json:"nodes"`
}

type CincinnatiNode struct {
	Version  string            `json:"version"`
	Payload  string            `json:"payload"`
	Metadata map[string]string `json:"metadata"`
}

func fetchGraph(channel string) (*CincinnatiGraph, error) {
	url := fmt.Sprintf("https://api.openshift.com/api/upgrades_info/graph?channel=%s", channel)
	resp, err := http.Get(url)
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

// compareVersions compares two version strings (e.g. "4.16.0" vs "4.16.1").
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
func compareVersions(a, b string) int {
	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")
	maxLen := len(partsA)
	if len(partsB) > maxLen {
		maxLen = len(partsB)
	}
	for i := 0; i < maxLen; i++ {
		var numA, numB int
		if i < len(partsA) {
			numA, _ = strconv.Atoi(partsA[i])
		}
		if i < len(partsB) {
			numB, _ = strconv.Atoi(partsB[i])
		}
		if numA < numB {
			return -1
		} else if numA > numB {
			return 1
		}
	}
	return 0
}

func isVersionNewer(a, b string) bool {
	return compareVersions(a, b) >= 0
}

// extractVersionFromChannel extracts the version part from a channel name by removing the prefix.
// For example, "stable-4.16" with prefix "stable-" returns "4.16".
func extractVersionFromChannel(channel, prefix string) string {
	return strings.TrimSpace(channel[len(prefix):])
}

func main() {
	startChannel := flag.String("channel", "stable-4.16", "Starting channel (e.g. stable-4.16)")
	minVersion := flag.String("min", "4.16.0", "Minimal version (e.g. 4.16.0 or 4.16.1)")
	prefixesArg := flag.String("prefixes", "fast-,stable-", "Channel prefixes separated by comma")
	flag.Parse()

	prefixes := strings.Split(*prefixesArg, ",")

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
		graph, err := fetchGraph(currentChannel)
		if err != nil {
			fmt.Printf("Error fetching graph for channel %s: %v\n", currentChannel, err)
			continue
		}

		if _, exists := nodesByChannel[currentChannel]; !exists {
			nodesByChannel[currentChannel] = make(map[string]CincinnatiNode)
		}

		for _, node := range graph.Nodes {
			if isVersionNewer(node.Version, *minVersion) {
				nodesByChannel[currentChannel][node.Version] = node
			}

			if channels, ok := node.Metadata["io.openshift.upgrades.graph.release.channels"]; ok {
				for _, ch := range strings.Split(channels, ",") {
					ch = strings.TrimSpace(ch)
					// Check if the channel starts with one of the specified prefixes.
					for _, prefix := range prefixes {
						if strings.HasPrefix(ch, prefix) {
							versionPart := extractVersionFromChannel(ch, prefix)
							// Only consider channels with version >= minimal version.
							if compareVersions(versionPart, *minVersion) >= 0 {
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
			return compareVersions(nodes[i].Version, nodes[j].Version) < 0
		})
		fmt.Printf("Channel %s:\n", channel)
		for _, node := range nodes {
			fmt.Printf("  Version: %s, Payload: %s\n", node.Version, node.Payload)
		}
	}

	// Aggregate and display unique releases per channel type.
	// For each prefix, merge nodes from all channels that start with that prefix.
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
			return compareVersions(nodes[i].Version, nodes[j].Version) < 0
		})
		fmt.Printf("Channel type %s:\n", prefix)
		for _, node := range nodes {
			fmt.Printf("  Version: %s, Payload: %s\n", node.Version, node.Payload)
		}
	}
}
