package redis

import (
	"strconv"
	"time"
)

const timeLayout = time.RFC3339Nano

func atoi(value string) int {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return parsed
}

func labelOrId(label, id string) string {
	if label != "" {
		return label
	}
	return id
}

func parseTime(value string) time.Time {
	parsed, err := time.Parse(timeLayout, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
