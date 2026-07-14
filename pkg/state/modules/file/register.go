// Package filemod implements the file.* state-module family.
package filemod

import "github.com/nirnx/zester/pkg/state/modules/regdef"

// Rows is the file.* family's slice of the built-in state-module table, in the
// family's historical registration order. The aggregator (pkg/state/modules)
// concatenates every family's Rows into the full table.
var Rows = []regdef.Registration{
	// file.managed — self-documenting schema (the BD-1 flagship: template on
	// paramtypes.TemplateFlag, mode on paramtypes.FileMode with a lazy 0644
	// default). NewFileManagedBuilder is itself a BuildFunc (it threads opts into
	// spec.Decode), so no providerBuild adapter.
	{Name: "file.managed", Spec: fileManagedSpec, Build: NewFileManagedBuilder},
	// file.directory — self-documenting schema (the declared-facet exemplar:
	// mode via the family component (member default 0755) with a standalone dir_mode fallback field).
	// NewFileDirectoryBuilder is itself a BuildFunc, so no providerBuild adapter.
	{Name: "file.directory", Spec: fileDirectorySpec, Build: NewFileDirectoryBuilder},
	// file.absent / file.append — self-documenting schema (all-primitives /
	// StringList wave). Both builders are themselves BuildFuncs, so no adapter.
	{Name: "file.absent", Spec: fileAbsentSpec, Build: NewFileAbsentBuilder},
	{Name: "file.append", Spec: fileAppendSpec, Build: NewFileAppendBuilder},
	// file.symlink — self-documenting schema (all-primitives wave).
	// NewFileSymlinkBuilder is itself a BuildFunc, so no adapter.
	{Name: "file.symlink", Spec: fileSymlinkSpec, Build: NewFileSymlinkBuilder},
	// file.blockreplace — self-documenting schema (all-primitives / marker
	// wave). NewFileBlockReplaceBuilder is itself a BuildFunc, so no adapter.
	{Name: "file.blockreplace", Spec: fileBlockReplaceSpec, Build: NewFileBlockReplaceBuilder},
	// file.recurse — self-documenting schema (the declared-only facet exemplar:
	// dir_mode DECLARED-ONLY on a lazy paramtypes.FileMode, file_mode lazy 0644).
	// NewFileRecurseBuilder is itself a BuildFunc, so no providerBuild adapter.
	{Name: "file.recurse", Spec: fileRecurseSpec, Build: NewFileRecurseBuilder},
	// file.line / file.replace / file.keyvalue — self-documenting schema (the
	// file-surgery wave: file.line's `mode` is an action enum, file.replace's
	// `pattern` is required with a builder-tail regex compile, file.keyvalue's
	// `key_values` is a paramtypes.StringMap with a module-local key/value
	// merge). Each builder is itself a BuildFunc, so no providerBuild adapter.
	{Name: "file.line", Spec: fileLineSpec, Build: NewFileLineBuilder},
	{Name: "file.replace", Spec: fileReplaceSpec, Build: NewFileReplaceBuilder},
	// file.comment / file.uncomment — the N:1 exemplar: one FileComment proto,
	// two Specs (one per registered name), each with its own documentation.
	{Name: "file.comment", Spec: fileCommentSpec, Build: NewFileCommentBuilder},
	{Name: "file.uncomment", Spec: fileUncommentSpec, Build: NewFileUncommentBuilder},
	{Name: "file.keyvalue", Spec: fileKeyValueSpec, Build: NewFileKeyValueBuilder},
	// file.copy / file.touch — self-documenting schema (all-primitives wave;
	// file.copy's source is `required`). Both builders are themselves
	// BuildFuncs, so no adapter.
	{Name: "file.copy", Spec: fileCopySpec, Build: NewFileCopyBuilder},
	{Name: "file.touch", Spec: fileTouchSpec, Build: NewFileTouchBuilder},
}
