package client

import (
	"slices"
	"strings"
)

func AggregateReleasesByChannelGroup(releasesByChannel ReleasesByChannel) ReleasesByChannel {
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
