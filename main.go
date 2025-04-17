package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"sort"

	"github.com/Masterminds/semver/v3"
	"github.com/p0lyn0mial/cincinnati-installation-versions/cincinnati-client"
)

func main() {
	startChannel := flag.String("channel", "fast-4.16", "Starting channel (e.g. stable-4.16)")
	flag.Parse()

	u, err := url.Parse("https://api.openshift.com/api/upgrades_info/graph")
	if err != nil {
		fmt.Printf("error parsing URL: %s\n", err)
		return
	}

	var allowedConditionalEdgeRisks []string
	hClient := &http.Client{}

	cincinnatiClient := cincinnaticlient.New(hClient)
	multiArchReleasesByChannel, err := cincinnatiClient.DiscoverReleases(u, *startChannel, "multi", allowedConditionalEdgeRisks)
	if err != nil {
		fmt.Printf("error discovering releases from %s: %v\n", *startChannel, err)
		return
	}

	aggregatedMultiArchReleasesByChannelGroup := cincinnaticlient.AggregateReleasesByChannelGroup(multiArchReleasesByChannel)
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
