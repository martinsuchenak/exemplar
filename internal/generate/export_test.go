package generate

// SetMaxRecordsBytes lowers the request size cap for a test, returning a
// function that restores it.
func SetMaxRecordsBytes(n int) func() {
	old := maxRecordsBytes
	maxRecordsBytes = n
	return func() { maxRecordsBytes = old }
}
