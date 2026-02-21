package common

const (
	// Cache Keys Prefix
	CacheKeyPrefix    = "oj:cache:"
	ResultKeyPrefix   = "result:"
	InFlightKeyPrefix = "oj:inflight:"

	// Streams-only 任务载荷键前缀
	TaskPayloadPrefix         = "task:payload:"

	// Job Status
	StatusPending    = "pending"
	StatusProcessing = "processing"
	StatusFinished   = "Finished" // Kept as specific string for compatibility
)
