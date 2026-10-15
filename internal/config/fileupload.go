package config

// Upload limits, matching the reference: the two caps are static server-level
// limits that bound what a single request may push through the ingress, so they
// are configuration rather than per-bank policy.
const (
	// DefaultRetainAttachmentMaxSizeMB bounds one attachment.
	DefaultRetainAttachmentMaxSizeMB = 20
	// DefaultRetainAttachmentMaxCount bounds how many one request may carry.
	DefaultRetainAttachmentMaxCount = 50
)

// resolveUploadLimits reads the upload caps from the environment.
//
// A non-positive value disables the corresponding cap, which is what an operator
// asks for when they raise the limit past what an integer holds rather than
// meaning "bound it at zero".
func resolveUploadLimits(lookup func(string) (string, bool)) (maxSizeMB, maxCount int) {
	maxSizeMB = intFromEnv(lookup, EnvRetainAttachmentMaxSizeMB, DefaultRetainAttachmentMaxSizeMB)
	maxCount = intFromEnv(lookup, EnvRetainAttachmentMaxCount, DefaultRetainAttachmentMaxCount)
	return maxSizeMB, maxCount
}
