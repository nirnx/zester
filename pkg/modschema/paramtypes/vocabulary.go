package paramtypes

// VocabularySize is the number of sealed semantic types compiled into the
// vocabulary. The conformance test pins len(All()) to this value so adding or
// dropping a type without updating the fixtures and count is caught.
const VocabularySize = 6

// init registers the sealed vocabulary. Registration is unexported (register),
// so only paramtypes-owned types can enter, and it runs before Seal() (called at
// production startup in a later tranche), so the sealed set is exactly this list.
// Duplicate names or Go types panic at load — a programming error, never a
// runtime condition.
func init() {
	register(fileModeType{})
	register(triStateType{})
	register(groupRefType{})
	register(templateFlagType{})
	register(stringListType{})
	register(stringMapType{})
}
