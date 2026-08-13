package edition

// AddQualcommFleetDeviceForTest registers a reviewed (marketing name, SoC)
// pair for the duration of a test — shared fixtures carry marketing SoC names
// that are not part of the shipped reviewed fleet. It returns a cleanup that
// removes the pair again.
func AddQualcommFleetDeviceForTest(marketingName, soc string) func() {
	key := identity(marketingName, soc)
	qualcommFleet[key] = struct{}{}
	return func() { delete(qualcommFleet, key) }
}
