// Package fs holds the real file-system adapters: FileOps, the only writer of
// skill folders, the store, staging and snapshots; LinkOps, which moves
// folders and creates or removes symlinks without ever replacing a path
// (docs/design-v2.md section 11, items 5 and 6); and SkillDirScanner, which
// lists a skills directory without changing it (docs/design.md section 5).
//
// All of them refuse any path that is relative or under a forbidden prefix,
// with errors matching paths.ErrInvalidPath and paths.ErrForbidden.
package fs
