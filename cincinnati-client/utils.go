package cincinnaticlient

import (
	"slices"
	"strings"
)

func AggregateReleasesByChannelGroupAndSortAvailableUpgrades(releasesByChannel ReleasesByChannel) (ReleasesByChannel, error) {
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
			releaseToAdd := release
			if existing, exists := aggregated[group][version]; exists {
				for _, up := range release.AvailableUpgrades {
					if !slices.Contains(existing.AvailableUpgrades, up) {
						existing.AvailableUpgrades = append(existing.AvailableUpgrades, up)
					}
				}
				releaseToAdd = existing
			}
			if err := releaseToAdd.SortAvailableUpgrades(); err != nil {
				return nil, err
			}
			aggregated[group][version] = releaseToAdd
		}
	}
	return aggregated, nil
}
