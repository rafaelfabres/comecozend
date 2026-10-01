package rapatches

// SetRawBaseForTest points archive downloads at another host. Only tests
// call it; the returned function restores the real one.
func SetRawBaseForTest(base string) func() {
	previous := rawBase
	rawBase = base
	return func() { rawBase = previous }
}
