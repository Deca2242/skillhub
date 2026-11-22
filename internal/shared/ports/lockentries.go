package ports

import (
	"errors"

	"skillhub/internal/shared/domain"
)

// Version 2: whole-entry moves of the npx skills global lock, under the
// exception the user approved in L32. The ref of an entry that stays in the
// lock is never added, changed or removed; a whole entry may leave the lock
// with its ref and come back identical and in the same position; an added
// entry never carries a ref. Every method refuses a lock whose version is not
// 3 (ErrUnsupportedLockVersion) and writes atomically after rereading.

var (
	// ErrLockEntryExists reports an entry that is already in the lock.
	ErrLockEntryExists = errors.New("skills lock entry already exists")
	// ErrLockMoved reports a lock that changed since the parked entry or the
	// expected entry was read, so the operation is refused.
	ErrLockMoved = errors.New("skills lock changed since it was read")
	// ErrRefNotAllowed reports an added entry that carries a ref.
	ErrRefNotAllowed = errors.New("an added skills lock entry may not carry a ref")
	// ErrMalformedLock reports a lock, or an entry, whose JSON or shape is
	// not what npx skills writes.
	ErrMalformedLock = errors.New("malformed skills lock")
)

// LockEntries takes whole entries out of the global lock and puts them back.
type LockEntries interface {
	// Park removes the entry name from the lock and returns it exactly as it
	// was: its raw bytes, its neighbours and the hash of the lock before
	// the change.
	Park(name string) (domain.ParkedEntry, error)
	// Unpark puts a parked entry back, byte for byte, between the same
	// neighbours. It fails with ErrLockEntryExists when name is in the lock
	// again, and with ErrLockMoved when a neighbour is gone.
	Unpark(entry domain.ParkedEntry) error
	// Add appends a new entry. It fails with ErrLockEntryExists when name is
	// in the lock and with ErrRefNotAllowed when raw carries a ref. A
	// missing lock file is created as an empty version 3 lock first.
	Add(name string, raw []byte) error
	// Remove takes the entry name out of the lock only when its raw bytes
	// are still expectedRaw; otherwise it fails with ErrLockMoved.
	Remove(name string, expectedRaw []byte) (domain.ParkedEntry, error)
	// Hash returns the SHA-256 of the lock file bytes, or "" when it does
	// not exist.
	Hash() (string, error)
}
