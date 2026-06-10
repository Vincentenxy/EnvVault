package postgres

import (
	"context"
	"database/sql"
	"strings"
	"sync"
)

type UserCache struct {
	mu     sync.RWMutex
	labels map[string]string
}

func NewUserCache() *UserCache {
	return &UserCache{labels: make(map[string]string)}
}

func (c *UserCache) Load(ctx context.Context, db *sql.DB) error {
	if c == nil || db == nil {
		return nil
	}
	rows, err := db.QueryContext(ctx, `
select id, name
from users
`)
	if err != nil {
		return err
	}
	defer rows.Close()

	labels := make(map[string]string)
	for rows.Next() {
		var userId, name string
		if err := rows.Scan(&userId, &name); err != nil {
			return err
		}
		userId = strings.TrimSpace(userId)
		if userId == "" {
			continue
		}
		labels[userId] = userLabel(userId, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	c.labels = labels
	c.mu.Unlock()
	return nil
}

func (c *UserCache) CacheUserLabel(userId, name string) {
	if c == nil {
		return
	}
	userId = strings.TrimSpace(userId)
	if userId == "" {
		return
	}
	c.mu.Lock()
	c.labels[userId] = userLabel(userId, name)
	c.mu.Unlock()
}

func (c *UserCache) Label(userId string) string {
	userId = strings.TrimSpace(userId)
	if userId == "" {
		return ""
	}
	if c == nil {
		return userId
	}
	c.mu.RLock()
	label, ok := c.labels[userId]
	c.mu.RUnlock()
	if ok && strings.TrimSpace(label) != "" {
		return label
	}
	return userId
}

func userLabel(userId, name string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	return userId
}
