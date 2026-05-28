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
