package patch

import "errors"

// ErrNoSourceCRC means the format does not record what it was built
// against, so the base ROM can only be identified from the readme.
var ErrNoSourceCRC = errors.New("this patch format carries no base-ROM checksum")

// SourceCRC32 returns the CRC32 of the base ROM a patch expects.
//
// This is the most reliable way to pick the right dump out of a ROM
// folder: it comes from the patch itself, so it is right even when the
// readme beside it is wrong — and readmes in RAPatches are sometimes
// wrong, naming an unrelated game as the base. BPS and UPS both store it
// in a 12-byte footer; IPS and xdelta store nothing.
func SourceCRC32(patchData []byte) (uint32, error) {
	switch Detect(patchData) {
	case BPS, UPS:
		if len(patchData) < 12 {
			return 0, errors.New("patch is too short to hold a footer")
		}
		return le32(patchData[len(patchData)-12 : len(patchData)-8]), nil
	}
	return 0, ErrNoSourceCRC
}

// TargetCRC32 returns the CRC32 the patched ROM must have, for the same
// formats. Useful for reporting a mismatch in plain terms before the MD5
// comparison against RetroAchievements happens.
func TargetCRC32(patchData []byte) (uint32, error) {
	switch Detect(patchData) {
	case BPS, UPS:
		if len(patchData) < 12 {
			return 0, errors.New("patch is too short to hold a footer")
		}
		return le32(patchData[len(patchData)-8 : len(patchData)-4]), nil
	}
	return 0, ErrNoSourceCRC
}
