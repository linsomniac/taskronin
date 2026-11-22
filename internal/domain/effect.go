package domain

// EffectImplied reports whether a deletion or restore of the same task
// already accounts for this effect in the operation's display.
// AIDEV-NOTE: J§4.4 is shared by cli.printOp, cli.printAIChanges and web.flashLines.
func EffectImplied(kind string, deleted, undeleted bool) bool {
	switch kind {
	case "placed", "moved", "updated":
		return deleted || undeleted
	case "reason_removed", "undeferred":
		return deleted
	default:
		return false
	}
}
