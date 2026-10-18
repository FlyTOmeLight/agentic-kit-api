package durable

// StreamKey names the notification stream for one run. The database stays the
// replay source; the stream only accelerates live delivery.
func StreamKey(id string) string { return "durable:run:" + id + ":events" }
