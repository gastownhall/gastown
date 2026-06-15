package beads

// MigrateInfraToWisps migrates infra types to wisps.
func MigrateInfraToWisps(beads []Bead) []Wisp {
	var wisps []Wisp
	for _, bead := range beads {
		if !IsInfraType(bead.Type) {
			wisps = append(wisps, Wisp{
				Type: bead.Type,
				Data: bead.Data,
			})
		}
	}
	return wisps
}

// IsInfraType checks if a type is an infra type.
func IsInfraType(typ string) bool {
	for _, infraType := range InfraTypes {
		if typ == infraType {
			return true
		}
	}
	return false
}
