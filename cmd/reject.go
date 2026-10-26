package cmd

import "github.com/paularlott/cli"

func init() {
	Register(rejectCmd())
}

// rejectCmd rejects real-derived fixtures that failed review. Admin key.
// Rejected fixtures stay in quarantine and are never copied out.
func rejectCmd() *cli.Command {
	return reviewCmd("reject", "Reject real-derived fixtures that failed review (admin key)", false)
}
