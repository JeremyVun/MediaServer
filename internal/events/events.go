// Package events is the internal typed pub/sub bus. Every state change a
// client could care about is published here; the SSE hub and the logger
// subscribe and fan out.
//
// Delivery is best-effort by design: a slow subscriber has its oldest
// pending event dropped rather than ever blocking a publisher. Clients heal
// missed events by refetching (see SPEC-API.md, events section).
package events

// Event types. Payload shapes follow SPEC-API.md's SSE contract.
const (
	ItemAdded      = "item.added"
	ItemUpdated    = "item.updated"
	ItemRemoved    = "item.removed"
	FileStatus     = "file.status"
	RootStatus     = "root.status"
	UploadProgress = "upload.progress"
	UploadComplete = "upload.complete"
	JobProgress    = "job.progress"
)

type Event struct {
	Type    string
	Payload any
}
