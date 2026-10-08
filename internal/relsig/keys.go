package relsig

// ReleaseKeys are the public keys allowed to sign checksums.txt. Add the new
// key before rotating, and keep the old one for as long as releases signed
// with it should still verify. TestReleaseKeysConfigured fails while the list
// is empty, so a release cannot ship without one.
//
// The first key signs releases (its private half is the
// FFM_RELEASE_SIGNING_KEY secret). The second is an offline backup: its
// private half is never on a build machine, so it can take over if the first
// leaks or is lost, without users having to reinstall ffm by hand.
var ReleaseKeys = []string{
	"6Dsj8Qv2qhi6zYV3LKZqRdkYeFifgEDbr0r+qYE4Jjs=",
	"z++AVagHrnyuwSeBHaQULVBwnf0NodJd1r4+LMEsL18=",
}
